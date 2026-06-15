import { useEffect, useMemo, useState } from 'react'
import {
  createPathTemplate,
  deletePathTemplate,
  fetchPathTemplates,
  saveEnrichMetadata,
  savePathTemplate,
} from '../../lib/api'
import { Button, TextField } from './common'
import { ToggleRow } from './Toggle'

const tokenHelp = [
  '{{artist}}',
  '{{album}}',
  '{{track}}',
  '{{title}}',
]

export function PathTemplateModal({ open, value, enrichEnabled = false, onClose, onChange, onEnrichChange }) {
  const [template, setTemplate] = useState(value || '')
  const [enrich, setEnrich] = useState(!!enrichEnabled)
  const [templates, setTemplates] = useState([])
  const [name, setName] = useState('')
  const [status, setStatus] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    if (!open) return
    setTemplate(value || '')
    setEnrich(!!enrichEnabled)
    setStatus('')
    setError('')
    loadTemplates()
  }, [open, value, enrichEnabled])

  async function loadTemplates() {
    try {
      setTemplates(await fetchPathTemplates())
    } catch (err) {
      setError(err.message || String(err))
    }
  }

  const selected = useMemo(() => templates.find(item => item.template === template), [templates, template])

  if (!open) return null

  async function applyTemplate() {
    setError('')
    try {
      await savePathTemplate(template)
      await saveEnrichMetadata(enrich)
      onChange?.(template)
      onEnrichChange?.(enrich)
      onClose?.()
    } catch (err) {
      setError(err.message || String(err))
    }
  }

  async function savePreset() {
    if (!name.trim() || !template.trim()) return
    setError('')
    try {
      await createPathTemplate(name.trim(), template.trim())
      setName('')
      setStatus('Preset saved.')
      await loadTemplates()
    } catch (err) {
      setError(err.message || String(err))
    }
  }

  async function removePreset(item) {
    if (item.built_in) return
    setError('')
    try {
      await deletePathTemplate(item.name)
      setStatus('Preset deleted.')
      await loadTemplates()
    } catch (err) {
      setError(err.message || String(err))
    }
  }

  return (
    <div className="fixed inset-0 z-50 bg-black/70 flex items-center justify-center p-4" onMouseDown={onClose}>
      <div className="w-full max-w-3xl bg-panel border border-ui-border rounded-[12px] shadow-card p-5" onMouseDown={e => e.stopPropagation()}>
        <div className="flex items-start justify-between gap-4 mb-5">
          <div>
            <div className="text-[11px] text-muted uppercase tracking-[1px] mb-1">Path Template</div>
            <h2 className="text-[18px] text-white font-semibold">Configure track paths</h2>
            <p className="text-[12px] text-muted mt-1">Pick a preset or build a custom template for downloaded track paths.</p>
          </div>
          <button className="text-muted hover:text-white text-[20px] leading-none" onClick={onClose}>×</button>
        </div>

        {error && <div className="mb-3 text-[12px] text-danger whitespace-pre-wrap">{error}</div>}
        {status && !error && <div className="mb-3 text-[12px] text-muted">{status}</div>}

        <div className="grid lg:grid-cols-[1fr_240px] gap-4">
          <div className="grid gap-4">
            <TextField label="Template" hint="Example: {{artist}}/{{album}}/{{track}} - {{title}}">
              <input
                className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2.5 text-[14px] text-white font-mono outline-none focus:border-accent"
                value={template}
                onChange={e => setTemplate(e.target.value)}
                placeholder="{{artist}}/{{album}}/{{title}}"
                autoComplete="off"
                spellCheck={false}
              />
            </TextField>

            <div className="flex flex-wrap gap-2">
              {tokenHelp.map(token => (
                <button
                  key={token}
                  type="button"
                  onClick={() => setTemplate(prev => `${prev}${prev && !prev.endsWith('/') ? '/' : ''}${token}`)}
                  className="rounded-full border border-ui-border px-3 py-1.5 text-[12px] text-muted hover:text-white hover:border-accent transition-colors"
                >
                  {token}
                </button>
              ))}
            </div>

            <ToggleRow
              checked={enrich}
              onChange={setEnrich}
              name="Enrich track metadata"
              desc="Fetch additional metadata before applying path template tokens."
            />

            <div className="grid sm:grid-cols-[1fr_auto] gap-2 items-end">
              <TextField label="Save current template as preset">
                <input
                  className="w-full bg-well border border-ui-border rounded-[8px] px-3 py-2 text-[13px] text-white outline-none focus:border-accent"
                  value={name}
                  onChange={e => setName(e.target.value)}
                  placeholder="My folder layout"
                />
              </TextField>
              <Button onClick={savePreset} disabled={!name.trim() || !template.trim()} className="py-2">Save preset</Button>
            </div>
          </div>

          <div className="bg-well border border-ui-border rounded-[8px] p-2 max-h-[360px] overflow-y-auto">
            {templates.length === 0 ? (
              <p className="text-[12px] text-muted p-2">No presets yet.</p>
            ) : templates.map(item => (
              <button
                key={item.name}
                type="button"
                onClick={() => setTemplate(item.template)}
                className={`w-full text-left rounded-[7px] p-2 mb-1 border transition-colors ${selected?.name === item.name ? 'border-accent bg-accent/10' : 'border-transparent hover:border-ui-border hover:bg-white/5'}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-[12px] text-white font-medium">{item.name}</span>
                  {!item.built_in && <span onClick={e => { e.stopPropagation(); removePreset(item) }} className="text-[11px] text-danger hover:text-white">Delete</span>}
                </div>
                <div className="text-[11px] text-muted font-mono break-all mt-1">{item.template}</div>
              </button>
            ))}
          </div>
        </div>

        <div className="flex justify-end gap-2 mt-5">
          <Button onClick={onClose} className="bg-transparent">Cancel</Button>
          <Button onClick={applyTemplate}>Apply template</Button>
        </div>
      </div>
    </div>
  )
}
