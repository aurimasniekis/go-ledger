import { useState } from 'react'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowOnceDialog } from '@/components/show-once-dialog'

const SECRET = 'ldgr_' + 'A'.repeat(43)

// A host like the real pages: the secret lives in state only while the dialog is open.
function Host({ warning }: { warning?: string | null }) {
  const [secret, setSecret] = useState<string | null>(SECRET)
  return (
    <div>
      <button>before</button>
      {secret && (
        <ShowOnceDialog
          title="Token created"
          description="Copy it now."
          lines={[secret]}
          listLabel="API token"
          warning={warning}
          filename="token.txt"
          onDone={() => setSecret(null)}
        />
      )}
      <p>{secret ? 'open' : 'gone'}</p>
    </div>
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('ShowOnceDialog', () => {
  it('shows the secret once, labelled, as a modal dialog with focus inside', async () => {
    render(<Host />)
    const dlg = await screen.findByRole('dialog', { name: 'Token created' })
    expect(dlg).toHaveAccessibleDescription(/Copy it now/)
    expect(within(screen.getByRole('list', { name: 'API token' })).getByText(SECRET)).toBeInTheDocument()
    expect(dlg).toContainElement(document.activeElement as HTMLElement)
    expect(dlg).toHaveTextContent('only time it is shown')
  })

  it('shows the root warning when the server sent one', async () => {
    render(<Host warning="This token can do everything you can." />)
    expect(await screen.findByRole('note')).toHaveTextContent('can do everything')
  })

  it('does not close on Escape or on a click outside', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await screen.findByRole('dialog')
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    await user.click(document.querySelector('.bg-foreground\\/50') as HTMLElement)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(SECRET)).toBeInTheDocument()
  })

  it('closes with Done by keyboard, drops the secret, and cannot be reopened', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await screen.findByRole('dialog')
    for (let i = 0; i < 6 && document.activeElement !== screen.getByRole('button', { name: 'Done' }); i++) await user.tab()
    expect(screen.getByRole('button', { name: 'Done' })).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByText('gone')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain(SECRET)
    // Nothing on the page offers to show it again.
    expect(screen.queryByRole('button', { name: /show|reveal|reopen/i })).not.toBeInTheDocument()
  })

  it('copies the secret and says so', async () => {
    const user = userEvent.setup()
    render(<Host />)
    // userEvent.setup() installs its own clipboard stub.
    await user.click(await screen.findByRole('button', { name: 'Copy' }))
    expect(await screen.findByText('Copied to the clipboard.')).toBeInTheDocument()
    expect(await navigator.clipboard.readText()).toBe(SECRET)
  })

  it('says when copying fails instead of pretending it worked', async () => {
    const user = userEvent.setup()
    render(<Host />)
    await screen.findByRole('dialog')
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => Promise.reject(new Error('denied')) } })
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await screen.findByText(/Copying failed/)).toBeInTheDocument()
  })

  it('downloads the secret as a text file', async () => {
    const user = userEvent.setup()
    const create = vi.fn(() => 'blob:x')
    const revoke = vi.fn()
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke })
    render(<Host />)
    await user.click(await screen.findByRole('button', { name: 'Download' }))
    expect(create).toHaveBeenCalledOnce()
    const blob = (create.mock.calls[0] as unknown as [Blob])[0]
    expect(await blob.text()).toBe(SECRET + '\n')
    expect(revoke).toHaveBeenCalledWith('blob:x')
    expect(await screen.findByText('Download started.')).toBeInTheDocument()
  })
})
