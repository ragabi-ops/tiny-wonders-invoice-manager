// Package delivery gets issued documents to customers: by email, or by handing
// the operator a prepared WhatsApp message to send themselves (plan.md 14).
//
// The rule that shapes this package: a delivery failure must NEVER roll back a
// successfully issued document. Sending therefore happens after issuance, in a
// background job, in its own transaction. Every attempt is recorded, so a
// failure is visible rather than silent.
package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// ErrMailerUnavailable means email is not configured or could not be reached.
var ErrMailerUnavailable = errors.New("email is not available")

// Message is an email to send.
type Message struct {
	To         string
	Subject    string
	Body       string
	Attachment []byte
	AttachName string
	AttachMIME string
}

// Mailer sends email.
type Mailer interface {
	Send(ctx context.Context, message Message) error
	// Configured reports whether sending is possible at all, so the UI can hide
	// what cannot work rather than offering it and failing.
	Configured() bool
}

// SMTPConfig is what an SMTP mailer needs.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	// StartTLS upgrades a plaintext connection. Implicit TLS is used when the
	// port is 465.
	StartTLS bool
}

// SMTPMailer sends through an SMTP server.
type SMTPMailer struct {
	cfg SMTPConfig
}

// NewSMTPMailer builds a mailer. An empty host yields a mailer that reports
// itself unconfigured rather than failing at send time.
func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer { return &SMTPMailer{cfg: cfg} }

// Configured reports whether a host and sender address are set.
func (m *SMTPMailer) Configured() bool {
	return strings.TrimSpace(m.cfg.Host) != "" && strings.TrimSpace(m.cfg.From) != ""
}

// Send delivers one message with its PDF attached.
func (m *SMTPMailer) Send(ctx context.Context, message Message) error {
	if !m.Configured() {
		return fmt.Errorf("%w: SMTP is not configured", ErrMailerUnavailable)
	}
	if _, err := mail.ParseAddress(message.To); err != nil {
		return fmt.Errorf("invalid recipient %q: %w", message.To, err)
	}

	raw, err := m.build(message)
	if err != nil {
		return err
	}

	address := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))

	// A hung mail server must not hold a worker slot indefinitely.
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("%w: dial %s: %v", ErrMailerUnavailable, address, err)
	}

	if m.cfg.Port == 465 {
		conn = tls.Client(conn, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%w: %v", ErrMailerUnavailable, err)
	}
	defer client.Close()

	if m.cfg.StartTLS && m.cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("start tls: %w", err)
			}
		} else {
			// Refusing is deliberate: credentials and customer documents must
			// not cross the network in the clear.
			return fmt.Errorf("%w: server does not offer STARTTLS", ErrMailerUnavailable)
		}
	}

	if m.cfg.Username != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := client.Rcpt(message.To); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(raw); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close message: %w", err)
	}

	return client.Quit()
}

// build assembles a MIME message: a UTF-8 Hebrew body plus a PDF attachment.
func (m *SMTPMailer) build(message Message) ([]byte, error) {
	var out strings.Builder

	from := m.cfg.From
	if m.cfg.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", m.cfg.FromName) + " <" + m.cfg.From + ">"
	}

	boundary := "invoice-boundary-" + fmt.Sprint(time.Now().UnixNano())

	out.WriteString("From: " + from + "\r\n")
	out.WriteString("To: " + message.To + "\r\n")
	// Hebrew subjects must be encoded or they arrive as mojibake.
	out.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", message.Subject) + "\r\n")
	out.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	out.WriteString("MIME-Version: 1.0\r\n")

	if len(message.Attachment) == 0 {
		out.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		out.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		out.WriteString(wrapBase64(message.Body))
		return []byte(out.String()), nil
	}

	out.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")

	out.WriteString("--" + boundary + "\r\n")
	out.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	out.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	out.WriteString(wrapBase64(message.Body))
	out.WriteString("\r\n")

	name := message.AttachName
	if name == "" {
		name = "document.pdf"
	}
	mimeType := message.AttachMIME
	if mimeType == "" {
		mimeType = "application/pdf"
	}

	out.WriteString("--" + boundary + "\r\n")
	out.WriteString("Content-Type: " + mimeType + "; name=\"" + name + "\"\r\n")
	out.WriteString("Content-Transfer-Encoding: base64\r\n")
	out.WriteString("Content-Disposition: attachment; filename=\"" + name + "\"\r\n\r\n")
	out.WriteString(wrapBytesBase64(message.Attachment))
	out.WriteString("\r\n--" + boundary + "--\r\n")

	return []byte(out.String()), nil
}

// Unavailable is the mailer used when email is not configured. It refuses,
// so the UI can say so rather than silently dropping messages.
type Unavailable struct{}

// Configured reports false.
func (Unavailable) Configured() bool { return false }

// Send always fails.
func (Unavailable) Send(context.Context, Message) error {
	return fmt.Errorf("%w: set SMTP_HOST and SMTP_FROM to enable email", ErrMailerUnavailable)
}
