import { currencyName, formatAmount, formatDate } from '@/lib/format'

/**
 * A money amount: the sighted text is "1,124.19 EUR" and the spoken text is "1,124.19 Euro", so a screen
 * reader does not spell the code. The amount and currency stay together, and amounts of different
 * currencies are never combined here: this only displays what the server sent.
 */
export function Amount({ amount, currency, className }: { amount: string; currency: string; className?: string }) {
  const shown = formatAmount(amount)
  return (
    <span className={className}>
      <span aria-hidden data-amount="visual">
        {shown} {currency}
      </span>
      <span className="sr-only">
        {shown} {currencyName(currency)}
      </span>
    </span>
  )
}

/** A calendar date (YYYY-MM-DD) in the locale's format, in a <time> element carrying the ISO value. */
export function DateText({ value, className }: { value: string; className?: string }) {
  return (
    <time dateTime={value} className={className}>
      {formatDate(value)}
    </time>
  )
}
