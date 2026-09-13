/** Wire types mirroring the Go API. Money is always integer agorot. */

export type Role = 'OWNER' | 'OPERATOR' | 'ACCOUNTANT' | 'READ_ONLY';

export interface User {
  id: string;
  email: string;
  display_name: string;
  active: boolean;
  must_change_password: boolean;
  roles: Role[];
  last_login_at: string | null;
  created_at: string;
}

export interface MeResponse {
  user: User;
  roles: string[];
}

export interface BusinessProfile {
  legal_name: string;
  display_name: string;
  business_number: string;
  address: string;
  phone: string;
  email: string;
  website: string;
  bank_details: string;
  footer_note: string;
  updated_at: string;
  /** True when a logo has been uploaded; the bytes are served separately. */
  has_logo: boolean;
  logo_content_type?: string;
  logo_sha256?: string;
  logo_byte_size?: number;
  logo_uploaded_at?: string;
}

export interface ComplianceStatus {
  business_date: string;
  mode: 'EXEMPT_DEALER' | 'AUTHORIZED_DEALER';
  mode_hebrew: string;
  vat_applies: boolean;
  allowed_document_types: string[];
  real_issuance_enabled: boolean;
  unresolved_gate_items: string[];
  threshold_known: boolean;
  /** Annual exempt-dealer ceiling, in agorot. */
  threshold_agorot: number;
}

export interface AuditEvent {
  id: number;
  occurred_at: string;
  actor_user_id: string | null;
  actor_email: string | null;
  operation: string;
  entity_type: string | null;
  entity_id: string | null;
  request_id: string | null;
  reason: string | null;
  ip: string | null;
  before_summary?: unknown;
  after_summary?: unknown;
}

export interface RegulatoryEntry {
  key: string;
  effective_from: string;
  value: unknown;
  note: string | null;
  created_at: string;
}

export interface RoleOption {
  role: Role;
  hebrew: string;
}

// ---------------------------------------------------------------- Phase 2 --

export type CustomerType = 'PERSON' | 'BUSINESS';
export type DeliveryPreference = 'EMAIL' | 'WHATSAPP' | 'NONE';

export interface Customer {
  id: string;
  customer_type: CustomerType;
  display_name: string;
  legal_name: string;
  business_or_id_number: string;
  phone: string;
  email: string;
  address: string;
  notes: string;
  preferred_delivery: DeliveryPreference;
  active: boolean;
  created_at: string;
  updated_at: string;
}

/** The mutable half of a customer, as the create and update endpoints take it. */
export type CustomerInput = Pick<
  Customer,
  | 'customer_type'
  | 'display_name'
  | 'legal_name'
  | 'business_or_id_number'
  | 'phone'
  | 'email'
  | 'address'
  | 'notes'
  | 'preferred_delivery'
>;

export type DuplicateReason = 'PHONE' | 'EMAIL' | 'BUSINESS_NUMBER' | 'NAME';

export interface CustomerDuplicate {
  customer: Customer;
  reason: DuplicateReason;
  reason_hebrew: string;
}

export interface CustomerPage {
  customers: Customer[];
  total: number;
  limit: number;
  offset: number;
}

export interface ServiceItem {
  id: string;
  name: string;
  description: string;
  /** Default price in agorot. */
  default_price_agorot: number;
  unit: string;
  category: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

export type ServiceInput = Pick<
  ServiceItem,
  'name' | 'description' | 'default_price_agorot' | 'unit' | 'category'
>;

export interface ServicePage {
  services: ServiceItem[];
  total: number;
  limit: number;
  offset: number;
}

export type ActivityStatus = 'PLANNED' | 'ACTIVE' | 'DONE' | 'CANCELLED';

export interface Activity {
  id: string;
  name: string;
  start_at: string | null;
  end_at: string | null;
  location: string;
  status: ActivityStatus;
  notes: string;
  created_at: string;
  updated_at: string;
}

export type ActivityInput = Pick<Activity, 'name' | 'start_at' | 'end_at' | 'location' | 'status' | 'notes'>;

export interface ActivityPage {
  activities: Activity[];
  total: number;
  limit: number;
  offset: number;
}

export interface SearchHit {
  kind: 'CUSTOMER' | 'SERVICE' | 'ACTIVITY';
  id: string;
  title: string;
  subtitle: string;
}

export interface SearchResults {
  query: string;
  customers: SearchHit[];
  services: SearchHit[];
  activities: SearchHit[];
}

// ---------------------------------------------------------------- Phase 3 --

export type DocumentType = 'TRANSACTION_INVOICE' | 'PAYMENT_REQUEST' | 'RECEIPT';
export type DocumentState = 'DRAFT' | 'ISSUED' | 'CANCELLED';

export interface DocumentLine {
  id: string;
  line_number: number;
  service_id: string | null;
  description: string;
  unit: string;
  /** Quantity in integer thousandths: 1.5 is 1500. */
  quantity_milli: number;
  unit_price_agorot: number;
  line_total_agorot: number;
}

/** A line as submitted. The total is absent: the server computes it. */
export interface DocumentLineInput {
  service_id?: string | null;
  description: string;
  unit: string;
  quantity_milli: number;
  unit_price_agorot: number;
}

export interface InvoiceDocument {
  id: string;
  document_type: DocumentType;
  state: DocumentState;
  series: string;
  number: number | null;
  full_number: string;
  customer_id: string;
  customer_name: string;
  activity_id: string | null;
  activity_name: string | null;
  document_date: string;
  due_date: string | null;
  currency: string;
  subtotal_agorot: number;
  vat_agorot: number;
  total_agorot: number;
  notes: string;
  required_wording: string;
  issued_at: string | null;
  cancelled_at: string | null;
  cancellation_reason: string | null;
  pdf_sha256: string | null;
  pdf_bytes: number | null;
  template_version: string | null;
  test_mode: boolean;
  created_at: string;
  updated_at: string;
  lines: DocumentLine[];
}

export interface DocumentDraftInput {
  document_type: DocumentType;
  customer_id: string;
  activity_id?: string | null;
  document_date: string;
  due_date?: string | null;
  notes: string;
  lines: DocumentLineInput[];
}

export interface DocumentPage {
  documents: InvoiceDocument[];
  total: number;
  limit: number;
  offset: number;
}

export interface SequenceState {
  document_type: DocumentType;
  series: string;
  next_number: number;
  issued_count: number;
  highest_issued: number | null;
}

// ---------------------------------------------------------------- Phase 4 --

export type PaymentMethod =
  | 'CASH'
  | 'BANK_TRANSFER'
  | 'CREDIT_CARD'
  | 'BIT'
  | 'PAYBOX'
  | 'CHECK'
  | 'OTHER';

export interface PaymentAllocation {
  document_id: string;
  full_number: string;
  document_type: DocumentType;
  amount_agorot: number;
}

export interface PaymentAllocationInput {
  document_id: string;
  amount_agorot: number;
}

export interface Payment {
  id: string;
  customer_id: string;
  customer_name: string;
  amount_agorot: number;
  received_at: string;
  method: PaymentMethod;
  method_hebrew: string;
  reference: string;
  notes: string;
  activity_id: string | null;
  activity_name: string | null;
  receipt_document_id: string | null;
  receipt_number: string | null;
  allocated_agorot: number;
  on_account_agorot: number;
  allocations: PaymentAllocation[];
  created_at: string;
}

export interface PaymentInput {
  customer_id: string;
  amount_agorot: number;
  received_at: string;
  method: PaymentMethod;
  reference: string;
  notes: string;
  activity_id?: string | null;
  allocations: PaymentAllocationInput[];
  issue_receipt?: boolean;
}

export interface PaymentPage {
  payments: Payment[];
  total: number;
  limit: number;
  offset: number;
  total_amount_agorot: number;
}

/** What a customer owes and has paid. */
export interface CustomerBalance {
  customer_id: string;
  invoiced_agorot: number;
  paid_agorot: number;
  allocated_agorot: number;
  on_account_agorot: number;
  open_agorot: number;
}

export interface OutstandingDocument {
  id: string;
  document_type: DocumentType;
  full_number: string;
  document_date: string;
  total_agorot: number;
  allocated_agorot: number;
  outstanding_agorot: number;
}

export interface TimelineEntry {
  kind: 'DOCUMENT' | 'PAYMENT';
  id: string;
  date: string;
  title: string;
  full_number: string;
  amount_agorot: number;
  state: string;
  detail: string;
}

export interface MethodTotal {
  method: PaymentMethod;
  method_hebrew: string;
  count: number;
  total_agorot: number;
}

// ---------------------------------------------------------------- Phase 5 --

export interface ExpenseAttachment {
  id: string;
  original_filename: string;
  content_type: string;
  byte_size: number;
  sha256: string;
  uploaded_at: string;
}

export interface Expense {
  id: string;
  supplier: string;
  expense_date: string;
  amount_agorot: number;
  category: string;
  payment_method: PaymentMethod;
  payment_method_hebrew: string;
  reference: string;
  notes: string;
  activity_id: string | null;
  activity_name: string | null;
  attachments: ExpenseAttachment[];
  created_at: string;
  updated_at: string;
}

export type ExpenseInput = Pick<
  Expense,
  'supplier' | 'expense_date' | 'amount_agorot' | 'category' | 'payment_method' | 'reference' | 'notes'
> & { activity_id?: string | null };

export interface ExpensePage {
  expenses: Expense[];
  total: number;
  limit: number;
  offset: number;
  total_amount_agorot: number;
}

export interface CategoryTotal {
  category: string;
  count: number;
  total_agorot: number;
}

export interface DeliveryAttempt {
  id: string;
  document_id: string;
  channel: 'EMAIL' | 'WHATSAPP_LINK';
  recipient: string;
  state: 'QUEUED' | 'SENT' | 'FAILED';
  error: string | null;
  attempted_at: string;
}

export interface DeliveryStatus {
  email_available: boolean;
  whatsapp_available: boolean;
}

export interface WhatsAppShare {
  phone: string;
  message: string;
  share_url: string;
  download_url: string;
}

export interface RevenueRow {
  key: string;
  label: string;
  count: number;
  amount_agorot: number;
}

export interface UnpaidRow {
  document_id: string;
  full_number: string;
  document_type: DocumentType;
  customer_id: string;
  customer_name: string;
  document_date: string;
  due_date: string | null;
  total_agorot: number;
  paid_agorot: number;
  outstanding_agorot: number;
  days_overdue: number;
}

export interface Turnover {
  year: number;
  revenue_agorot: number;
  threshold_known: boolean;
  threshold_agorot: number;
  remaining_agorot: number;
  percent_used: number;
}

export interface Dashboard {
  business_date: string;
  revenue_today_agorot: number;
  revenue_month_agorot: number;
  revenue_year_agorot: number;
  unpaid_count: number;
  unpaid_amount_agorot: number;
  overdue_count: number;
  expenses_month_agorot: number;
  failed_deliveries: number;
  failed_jobs: number;
  turnover: Turnover;
  recent_documents: {
    id: string;
    full_number: string;
    document_type: DocumentType;
    customer_name: string;
    document_date: string;
    total_agorot: number;
    state: DocumentState;
  }[];
  recent_payments: {
    id: string;
    customer_name: string;
    received_at: string;
    amount_agorot: number;
    method: PaymentMethod;
  }[];
}

export interface ExportStatus {
  job_id: string;
  state: 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED' | 'CANCELLED';
  attempts?: number;
  created_at?: string;
  filename?: string;
  bytes?: number;
  sha256?: string;
  error?: string;
}

// ---------------------------------------------------------------- Phase 6 --

export interface BackupRun {
  id: string;
  started_at: string;
  finished_at: string | null;
  state: 'RUNNING' | 'SUCCEEDED' | 'FAILED';
  archive_path: string | null;
  archive_bytes: number | null;
  archive_sha256: string | null;
  file_count: number | null;
  trigger: 'SCHEDULED' | 'MANUAL';
  error: string | null;
}

export interface BackupStatus {
  configured: boolean;
  directory?: string;
  retention_days: number;
  schedule_local?: string;
  last_succeeded_at: string | null;
  last_archive_bytes: number | null;
  hours_since_success: number | null;
  /** True when the newest good backup is older than 36 hours, or there is none. */
  stale: boolean;
  last_run_state: string | null;
  last_run_error: string | null;
  tools?: {
    pg_dump_major: number;
    server_major: number;
    version_mismatch: boolean;
    version_warning?: string;
  };
}
