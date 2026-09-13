/** Hebrew labels for machine identifiers coming from the API. */

import type {
  ActivityStatus,
  CustomerType,
  DeliveryPreference,
  DocumentState,
  PaymentMethod,
  Role,
} from '@/api/types';

export const ROLE_LABELS: Record<Role, string> = {
  OWNER: 'בעלים',
  OPERATOR: 'מפעיל',
  ACCOUNTANT: 'רואה חשבון',
  READ_ONLY: 'צפייה בלבד',
};

/** Audit operations. An unknown operation falls back to its identifier so the
 * log never hides an event just because a label is missing. */
const AUDIT_LABELS: Record<string, string> = {
  AUTH_LOGIN_SUCCEEDED: 'התחברות מוצלחת',
  AUTH_LOGIN_FAILED: 'ניסיון התחברות כושל',
  AUTH_LOGIN_BLOCKED: 'התחברות נחסמה',
  AUTH_LOGOUT: 'התנתקות',
  AUTH_PASSWORD_CHANGED: 'שינוי סיסמה',
  USER_CREATED: 'נוצר משתמש',
  USER_ROLES_CHANGED: 'שינוי תפקידים',
  USER_ACTIVATED: 'הפעלת משתמש',
  USER_DEACTIVATED: 'השבתת משתמש',
  SETTINGS_UPDATED: 'עדכון הגדרות',
  REGULATORY_CONFIG_UPDATED: 'עדכון הגדרה רגולטורית',
  CUSTOMER_CREATED: 'נוצר לקוח',
  CUSTOMER_UPDATED: 'עודכן לקוח',
  CUSTOMER_ARCHIVED: 'לקוח הועבר לארכיון',
  CUSTOMER_RESTORED: 'לקוח שוחזר מהארכיון',
  SERVICE_CREATED: 'נוצר שירות',
  SERVICE_UPDATED: 'עודכן שירות',
  SERVICE_ARCHIVED: 'שירות הועבר לארכיון',
  SERVICE_RESTORED: 'שירות שוחזר מהארכיון',
  ACTIVITY_CREATED: 'נוצרה פעילות',
  ACTIVITY_UPDATED: 'עודכנה פעילות',
  DOCUMENT_DRAFT_CREATED: 'נוצרה טיוטת מסמך',
  DOCUMENT_DRAFT_UPDATED: 'עודכנה טיוטת מסמך',
  DOCUMENT_DRAFT_DELETED: 'נמחקה טיוטת מסמך',
  DOCUMENT_ISSUED: 'הופק מסמך',
  DOCUMENT_CANCELLED: 'בוטל מסמך',
  PAYMENT_RECORDED: 'נרשם תשלום',
  RECEIPT_ISSUED: 'הופקה קבלה',
  EXPENSE_CREATED: 'נרשמה הוצאה',
  EXPENSE_UPDATED: 'עודכנה הוצאה',
  EXPENSE_DELETED: 'נמחקה הוצאה',
  EXPENSE_ATTACHMENT_ADDED: 'צורף קובץ להוצאה',
  EXPENSE_ATTACHMENT_REMOVED: 'הוסר קובץ מהוצאה',
  DELIVERY_QUEUED: 'שליחה נכנסה לתור',
  DELIVERY_SENT: 'מסמך נשלח',
  DELIVERY_FAILED: 'שליחה נכשלה',
  EXPORT_REQUESTED: 'התבקש ייצוא',
  EXPORT_BUILT: 'ייצוא הוכן',
  EXPORT_DOWNLOADED: 'ייצוא הורד',
};

export function auditLabel(operation: string): string {
  return AUDIT_LABELS[operation] ?? operation;
}

/** Compliance-gate items, as listed in plan.md section 2. */
const GATE_LABELS: Record<string, string> = {
  required_fields_and_wording: 'שדות ונוסח חובה בכל מסמך',
  receipt_timing_and_payment_details: 'מועד הפקת קבלה ופרטי תשלום',
  cancellation_and_correction: 'ביטול ותיקון מסמכים',
  computerized_document_and_signature: 'מסמך ממוחשב וחתימה',
  legal_retention: 'חובת שמירת מסמכים',
  software_registration_required: 'חובת רישום תוכנה',
  accountant_export_requirements: 'דרישות ייצוא לרואה חשבון',
};

export function gateLabel(item: string): string {
  return GATE_LABELS[item] ?? item;
}

export const DOCUMENT_TYPE_LABELS: Record<string, string> = {
  TRANSACTION_INVOICE: 'חשבונית עסקה',
  PAYMENT_REQUEST: 'דרישת תשלום',
  RECEIPT: 'קבלה',
};

export const CUSTOMER_TYPE_LABELS: Record<CustomerType, string> = {
  PERSON: 'לקוח פרטי',
  BUSINESS: 'עסק',
};

export const DELIVERY_LABELS: Record<DeliveryPreference, string> = {
  EMAIL: 'דוא״ל',
  WHATSAPP: 'וואטסאפ',
  NONE: 'ללא',
};

export const ACTIVITY_STATUS_LABELS: Record<ActivityStatus, string> = {
  PLANNED: 'מתוכננת',
  ACTIVE: 'פעילה',
  DONE: 'הסתיימה',
  CANCELLED: 'בוטלה',
};

/** Mantine badge colors per activity status. */
export const ACTIVITY_STATUS_COLORS: Record<ActivityStatus, string> = {
  PLANNED: 'blue',
  ACTIVE: 'teal',
  DONE: 'gray',
  CANCELLED: 'red',
};

export const DOCUMENT_STATE_LABELS: Record<DocumentState, string> = {
  DRAFT: 'טיוטה',
  ISSUED: 'הופק',
  CANCELLED: 'בוטל',
};

export const DOCUMENT_STATE_COLORS: Record<DocumentState, string> = {
  DRAFT: 'gray',
  ISSUED: 'teal',
  CANCELLED: 'red',
};

export const PAYMENT_METHOD_LABELS: Record<PaymentMethod, string> = {
  CASH: 'מזומן',
  BANK_TRANSFER: 'העברה בנקאית',
  CREDIT_CARD: 'כרטיס אשראי',
  BIT: 'ביט',
  PAYBOX: 'פייבוקס',
  CHECK: 'המחאה',
  OTHER: 'אחר',
};
