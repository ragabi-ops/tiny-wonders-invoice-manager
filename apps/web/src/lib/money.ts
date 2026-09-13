/**
 * Conversions between the agorot integers the API uses and the shekel decimals
 * a person types. Rounding happens here, once, at the edge — never in
 * arithmetic, which always runs on integers.
 */

/** Converts a shekel amount from a form field into agorot. */
export function shekelsToAgorot(shekels: number | string | null | undefined): number {
  const value = typeof shekels === 'string' ? Number.parseFloat(shekels) : shekels;
  if (value === null || value === undefined || Number.isNaN(value)) return 0;
  return Math.round(value * 100);
}

/** Converts agorot into the shekel number a form field shows. */
export function agorotToShekels(agorot: number | null | undefined): number {
  if (agorot === null || agorot === undefined) return 0;
  return agorot / 100;
}

/** Converts a quantity typed as a decimal into integer thousandths. */
export function quantityToMilli(quantity: number | string | null | undefined): number {
  const value = typeof quantity === 'string' ? Number.parseFloat(quantity) : quantity;
  if (value === null || value === undefined || Number.isNaN(value)) return 0;
  return Math.round(value * 1000);
}

/** Converts integer thousandths back into the number a form field shows. */
export function milliToQuantity(quantityMilli: number | null | undefined): number {
  if (quantityMilli === null || quantityMilli === undefined) return 0;
  return quantityMilli / 1000;
}
