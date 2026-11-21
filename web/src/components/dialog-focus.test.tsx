import { useState } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog, DialogContent, DialogTrigger } from '@/components/ui/dialog'
import { useFocusRestore } from '@/lib/focus'

// A controlled dialog, as the real pages open them: there is no <DialogTrigger>, so Radix alone would drop focus on BODY.
function Controlled({ removeOpener = false }: { removeOpener?: boolean }) {
  const [open, setOpen] = useState(false)
  const [gone, setGone] = useState(false)
  return (
    <div>
      <main id="main" tabIndex={-1}>main</main>
      {!gone && <button onClick={() => setOpen(true)}>Delete</button>}
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title="Delete it?"
        description="This cannot be undone."
        confirmLabel="Delete"
        onConfirm={async () => {
          if (removeOpener) setGone(true)
        }}
      />
    </div>
  )
}

describe('dialog focus restore', () => {
  it('returns focus to the control that opened a controlled dialog, after Cancel', async () => {
    render(<Controlled />)
    const opener = screen.getByRole('button', { name: 'Delete' })
    await userEvent.click(opener)
    expect(await screen.findByRole('alertdialog')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(opener).toHaveFocus())
  })

  it('returns focus to the opener after Escape', async () => {
    render(<Controlled />)
    const opener = screen.getByRole('button', { name: 'Delete' })
    opener.focus()
    await userEvent.keyboard('{Enter}')
    await screen.findByRole('alertdialog')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(opener).toHaveFocus())
  })

  it('falls back to the main region, never BODY, when the opener is gone', async () => {
    render(<Controlled removeOpener />)
    await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByRole('alertdialog')
    await userEvent.click(screen.getAllByRole('button', { name: 'Delete' }).at(-1)!)
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    await waitFor(() => expect(screen.getByRole('main')).toHaveFocus())
  })

  it('still works for a dialog with a trigger', async () => {
    render(
      <Dialog>
        <DialogTrigger asChild><button>Open</button></DialogTrigger>
        <DialogContent title="Hello"><button>Close me</button></DialogContent>
      </Dialog>,
    )
    const trigger = screen.getByRole('button', { name: 'Open' })
    await userEvent.click(trigger)
    await screen.findByRole('dialog')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(trigger).toHaveFocus())
  })
})

function Panel() {
  const [open, setOpen] = useState(false)
  const focus = useFocusRestore()
  return (
    <div>
      <main id="main" tabIndex={-1}>main</main>
      <button disabled={open} onClick={() => { focus.remember(); setOpen(true) }}>Open</button>
      {open && <button onClick={() => { setOpen(false); focus.restore() }}>Done</button>}
    </div>
  )
}

describe('useFocusRestore', () => {
  it('focuses the opener once it is usable again (it was disabled while open)', async () => {
    render(<Panel />)
    const open = screen.getByRole('button', { name: 'Open' })
    await userEvent.click(open)
    await userEvent.click(screen.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(open).toHaveFocus())
  })
})
