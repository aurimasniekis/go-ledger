import { useEffect, useRef, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { useAnnounce } from '@/components/announcer'
import { useFocusRestore } from '@/lib/focus'
import { useMonthSummary } from './api'
import { monthLabel } from './month'
import { useOnline } from '@/lib/connection'
import { MonthBoard, OFFLINE_NOTE_ID } from './month-board'
import { PaymentPanel, UndoNotice } from './payment-panel'
import type { ManualPayment, MonthItem } from './types'

/** The /month screen. Its one primary action is "Record a payment". */
export function MonthPage({ month }: { month: string }) {
  const navigate = useNavigate()
  const q = useMonthSummary(month)
  const online = useOnline()
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [])
  const [panel, setPanel] = useState<{ item: MonthItem; inPlace: boolean } | null>(null) // the occurrence the open form starts on, and whether it sits under that row
  // The notice belongs to the month the payment was recorded in, so Undo still refreshes that month after the user moves on.
  // It has no timeout (D37): it stays until dismissed or replaced by the next payment.
  const [saved, setSaved] = useState<{ payment: ManualPayment; item: MonthItem; month: string } | null>(null)
  const headerButton = useRef<HTMLButtonElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  // Give focus back to what opened the form, or to the primary button when that is gone; when nothing
  // focusable is left (every occurrence paid) it falls back to the month title, never to BODY.
  const focus = useFocusRestore(() => [headerButton.current, headingRef.current])
  const openPanel = (item: MonthItem | null, inPlace: boolean) => {
    focus.remember()
    setPanel(item ? { item, inPlace } : null)
  }
  // Moving to another month is announced (focus stays on the Previous/Next button the user pressed).
  const announce = useAnnounce()
  const shown = useRef(month)
  useEffect(() => {
    if (shown.current === month) return
    shown.current = month
    announce(`Showing ${monthLabel(month)}.`)
  }, [month, announce])
  const unpaid = (q.data?.items ?? []).filter((i) => i.payment.status !== 'paid' && i.payment.status !== 'cancelled')

  return (
    <section className="space-y-4" aria-labelledby="month-title">
      <header className="flex flex-wrap items-center justify-end gap-2">
        <Button ref={headerButton} className="h-auto min-h-9 whitespace-normal py-1.5" onClick={() => openPanel(unpaid[0] ?? null, false)} disabled={unpaid.length === 0 || panel !== null || !online} aria-describedby={online ? undefined : OFFLINE_NOTE_ID}>
          Record a payment
        </Button>
      </header>
      {!online && (
        <p id={OFFLINE_NOTE_ID} className="text-sm text-muted-foreground">
          Recording a payment is unavailable while you are offline: it could not be saved. It comes back with the connection.
        </p>
      )}
      {q.data && unpaid.length === 0 && (
        <p className="text-sm text-muted-foreground">
          {q.data.items.length > 0
            ? 'Every occurrence this month is marked paid, so there is nothing to record.'
            : 'No occurrences fall due this month, so there is nothing to record.'}
        </p>
      )}

      <MonthBoard
        offline={!online}
        notice={saved && <UndoNotice key={saved.payment.id} {...saved} shownMonth={month} onDone={() => {
          setSaved(null)
          focus.restore()
        }} />}
        month={month}
        status={q.status}
        data={q.data}
        error={q.error}
        isFetching={q.isFetching}
        dataUpdatedAt={q.dataUpdatedAt}
        now={now}
        titleRef={headingRef}
        onMonthChange={(m) => {
          setPanel(null)
          void navigate({ to: '/month', search: { month: m } })
        }}
        onRetry={() => void q.refetch()}
        onRecord={(i) => openPanel(i, true)}
        formFor={panel?.inPlace ? panel.item.id : null}
        form={
          panel !== null && q.data ? (
            <PaymentPanel
              month={month}
              asOf={q.data.as_of}
              items={unpaid}
              initialId={panel.item.id}
              initialItem={panel.item}
              chooser={!panel.inPlace}
              onClose={() => {
                setPanel(null)
                focus.restore()
              }}
              onSaved={(payment, item) => {
                setPanel(null)
                setSaved({ payment, item, month })
              }}
            />
          ) : null
        }
      />
    </section>
  )
}
