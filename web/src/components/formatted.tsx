import { currencyName, formatAmount, formatDate } from '@/lib/format'

/**
 * A money amount: the sighted text is "1 124.19 EUR" (fixed narrow no-break space grouping, D38-3a) and the spoken text
 * is "1 124.19 Euro", so a screen reader does not spell the code. The amount and currency stay together on one line,
 * and amounts of different currencies are never combined here: this only displays what the server sent.
 */
export function Amount({ amount, currency, className }: { amount: string; currency: string; className?: string }) {
  const shown = formatAmount(amount)
  return (
    <span className={`whitespace-nowrap ${className ?? ''}`.trim()}>
      <span aria-hidden data-amount="visual">
        {shown}&nbsp;{currency}
      </span>
      <span className="sr-only">
        {shown} {currencyName(currency)}
      </span>
    </span>
  )
}

/** A calendar date (YYYY-MM-DD), shown as ISO and never broken at a hyphen, in a <time> element carrying the same value. */
export function DateText({ value, className }: { value: string; className?: string }) {
  return (
    <time dateTime={value} className={`whitespace-nowrap ${className ?? ''}`.trim()}>
      {formatDate(value)}
    </time>
  )
}
