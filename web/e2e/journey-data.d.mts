export const LEDGER_PORT: number
export const BANK_PORT: number
export const JOURNEY_USER: string
export const JOURNEY_PASSWORD: string
export function journeyDates(now?: Date): { prevMonth: string; currentMonth: string; nextMonth: string; day: (d: number) => string }
