import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { useMonthSummary } from './api'
import { MonthBoard } from './month-board'
import { PaymentPanel, UndoNotice } from './payment-panel'
import type { ManualPayment, MonthItem } from './types'

/** The /month screen. Its one primary action is "Record a payment". */
export function MonthPage({ month }: { month: string }) {
  const navigate = useNavigate()
  const q = useMonthSummary(month)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [])
  const [panel, setPanel] = useState<string | null>(null) // the occurrence the open form starts on
  const [saved, setSaved] = useState<{ payment: ManualPayment; item: MonthItem } | null>(null)
  const opener = useRef<HTMLElement | null>(null)
  const headerButton = useRef<HTMLButtonElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const openPanel = (id: string | null) => {
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    setPanel(id)
  }
  // Give focus back to what opened the form, or to the primary button when that is gone.
  // It runs after the re-render, so a button that was disabled while the form was open is usable again;
  // when nothing focusable is left (every occurrence paid) it falls back to the heading, never to BODY.
  const [restore, setRestore] = useState(0)
  const restoreFocus = () => setRestore((n) => n + 1)
  useEffect(() => {
    if (restore === 0) return
    const usable = (el: HTMLElement | null) => (el?.isConnected && !(el as HTMLButtonElement).disabled ? el : null)
    const el = usable(opener.current) ?? usable(headerButton.current) ?? headingRef.current
    el?.focus()
  }, [restore])
  const unpaid = (q.data?.items ?? []).filter((i) => i.payment.status !== 'paid' && i.payment.status !== 'cancelled')

  return (
    <section className="space-y-4" aria-labelledby="month-title">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h1 id="month-title" ref={headingRef} tabIndex={-1} className="text-2xl font-semibold focus:outline-none">
          Monthly view
        </h1>
        <Button ref={headerButton} onClick={() => openPanel(unpaid[0]?.id ?? null)} disabled={unpaid.length === 0 || panel !== null}>
          Record a payment
        </Button>
      </header>
      {q.data && unpaid.length === 0 && q.data.items.length > 0 && (
        <p className="text-sm text-muted-foreground">Every occurrence this month is marked paid, so there is nothing to record.</p>
      )}

      {panel !== null && q.data && (
        <PaymentPanel
          month={month}
          asOf={q.data.as_of}
          items={unpaid}
          initialId={panel}
          onClose={() => {
            setPanel(null)
            restoreFocus()
          }}
          onSaved={(payment, item) => {
            setPanel(null)
            setSaved({ payment, item })
          }}
        />
      )}
      {saved && <UndoNotice key={saved.payment.id} {...saved} month={month} onDone={() => {
        setSaved(null)
        restoreFocus()
      }} />}

      <MonthBoard
        month={month}
        status={q.status}
        data={q.data}
        error={q.error}
        isFetching={q.isFetching}
        dataUpdatedAt={q.dataUpdatedAt}
        now={now}
        onMonthChange={(m) => {
          setPanel(null)
          void navigate({ to: '/month', search: { month: m } })
        }}
        onRetry={() => void q.refetch()}
        onRecord={(i) => openPanel(i.id)}
      />
    </section>
  )
}
