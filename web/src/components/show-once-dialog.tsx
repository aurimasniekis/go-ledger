import { useState } from 'react'
import { Download, Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Mono } from '@/components/mono'

/**
 * Shows a secret exactly once: an API token or a set of recovery codes. The parent holds the secret
 * only in its own state while this dialog is open and drops it in onDone, so it cannot be reopened;
 * nothing here writes it to a cache, storage or the URL. Escape and clicks outside do not close it,
 * because a stray key would lose a secret that cannot be shown again; the Done button does.
 */
export function ShowOnceDialog({
  title,
  description,
  lines,
  warning,
  filename,
  listLabel,
  onDone,
}: {
  title: string
  description: string
  lines: string[]
  warning?: string | null
  filename: string
  listLabel: string
  onDone: () => void
}) {
  const [note, setNote] = useState('')
  const text = lines.join('\n')

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setNote('Copied to the clipboard.')
    } catch {
      setNote('Copying failed. Select the text and copy it yourself.')
    }
  }
  function download() {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
    setNote('Download started.')
  }

  return (
    <Dialog open>
      <DialogContent
        title={title}
        description={description}
        onEscapeKeyDown={(e) => e.preventDefault()}
        onPointerDownOutside={(e) => e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
      >
        <p className="text-sm font-medium">
          This is the only time it is shown. Ledger keeps no copy that can show it again. Close this dialog with the
          Done button once you have stored it.
        </p>
        {warning && (
          <p role="note" className="rounded-md border border-status-overdue bg-status-overdue-bg p-3 text-sm text-status-overdue">
            {warning}
          </p>
        )}
        <ul aria-label={listLabel} className="grid gap-1 rounded-md border border-border bg-muted p-3 sm:grid-cols-2">
          {lines.map((l) => (
            <li key={l}>
              <Mono className="break-all select-all bg-transparent">{l}</Mono>
            </li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" onClick={() => void copy()}>
            <Copy aria-hidden className="size-4" />
            Copy
          </Button>
          <Button variant="outline" onClick={download}>
            <Download aria-hidden className="size-4" />
            Download
          </Button>
          <span role="status" className="text-xs text-muted-foreground">
            {note}
          </span>
        </div>
        <div className="flex justify-end">
          <Button onClick={onDone}>
            Done
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
