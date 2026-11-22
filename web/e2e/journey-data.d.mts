export const LEDGER_PORT: number
export const BANK_PORT: number
export const DEFERRAL_LEDGER_PORT: number
export const DEFERRAL_BANK_PORT: number
export const COMMITMENTS_LEDGER_PORT: number
export const COMMITMENTS_BANK_PORT: number
export const JOURNEY_USER: string
export const JOURNEY_PASSWORD: string
export function journeyDates(now?: Date): { prevMonth: string; currentMonth: string; nextMonth: string; day: (d: number) => string; prevMonthName: string }
export function deferralDates(now?: Date): { month: string; monthName: string; laterMonth: string; laterMonthName: string; due: string; within: string; carried: string }
