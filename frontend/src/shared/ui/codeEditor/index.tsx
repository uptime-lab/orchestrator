import {
  FC,
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

// A code editor for script-shaped params (run_script.script,
// manage_service.unit_content, write_file.content, ...). CodeMirror, not
// Monaco: the production CSP is script-src 'self' with no unsafe-eval, which
// Monaco needs — it passes in dev and fails behind nginx.
//
// Commit discipline: the parent's onChange re-serializes the whole graph
// (isDirty) and re-renders the canvas, so committing per keystroke makes a
// large script janky. The editor keeps LOCAL state and commits on a trailing
// debounce and on blur. Clicking any button (Save included) blurs this editor
// first, and React flushes discrete-event updates before the next event's
// handlers run, so the click handler always reads the committed value.

export type CodeLanguage = 'shell' | 'ini' | 'plain'

const CodeMirrorLazy = lazy(async () => {
  const [
    { default: CodeMirror },
    { StreamLanguage },
    legacyShell,
    legacyProps,
    { oneDark },
  ] = await Promise.all([
    import('@uiw/react-codemirror'),
    import('@codemirror/language'),
    import('@codemirror/legacy-modes/mode/shell'),
    import('@codemirror/legacy-modes/mode/properties'),
    import('@codemirror/theme-one-dark'),
  ])
  const languageExtensions = {
    shell: [StreamLanguage.define(legacyShell.shell)],
    // systemd units are ini-shaped; the properties mode covers sections and
    // key=value without pretending to know systemd's directive names.
    ini: [StreamLanguage.define(legacyProps.properties)],
    plain: [],
  }
  const Component: FC<{
    value: string
    language: CodeLanguage
    placeholder?: string
    minHeight?: string
    onChange: (next: string) => void
    onBlur: () => void
  }> = ({ value, language, placeholder, minHeight, onChange, onBlur }) => (
    <CodeMirror
      value={value}
      theme={oneDark}
      extensions={languageExtensions[language]}
      placeholder={placeholder}
      minHeight={minHeight ?? '6rem'}
      maxHeight="60vh"
      basicSetup={{
        lineNumbers: true,
        foldGutter: false,
        highlightActiveLine: false,
        searchKeymap: false,
      }}
      onChange={onChange}
      onBlur={onBlur}
      style={{ fontSize: '12px' }}
    />
  )
  return { default: Component }
})

export const CodeEditor: FC<{
  value: string
  language?: CodeLanguage
  placeholder?: string
  // Committed on blur and on a trailing debounce, never per keystroke.
  onCommit: (next: string) => void
}> = ({ value, language = 'plain', placeholder, onCommit }) => {
  const { t } = useTranslation()
  const [local, setLocal] = useState(value)
  const [expanded, setExpanded] = useState(false)
  const localRef = useRef(value)
  const committedRef = useRef(value)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // An outside change (another field cleared the param, a template reload)
  // replaces the draft — but only while the draft is already committed, so
  // typing is never clobbered by the echo of its own debounce.
  useEffect(() => {
    if (value !== committedRef.current) {
      committedRef.current = value
      localRef.current = value
      setLocal(value)
    }
  }, [value])

  const commit = useCallback(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
    if (localRef.current !== committedRef.current) {
      committedRef.current = localRef.current
      onCommit(localRef.current)
    }
  }, [onCommit])

  // Unmount flushes the draft: closing the inspector must not lose a script.
  useEffect(() => commit, [commit])

  const handleChange = useCallback(
    (next: string) => {
      localRef.current = next
      setLocal(next)
      if (timerRef.current) clearTimeout(timerRef.current)
      timerRef.current = setTimeout(commit, 400)
    },
    [commit],
  )

  const editor = (min?: string) => (
    <Suspense
      fallback={
        <div className="rounded border border-neutral-11 bg-neutral-13 px-2 py-1 font-mono text-xs text-neutral-6">
          {t('shared.codeEditor.loading')}
        </div>
      }
    >
      <CodeMirrorLazy
        value={local}
        language={language}
        placeholder={placeholder}
        minHeight={min}
        onChange={handleChange}
        onBlur={commit}
      />
    </Suspense>
  )

  return (
    <div className="flex flex-col gap-1">
      <div className="overflow-hidden rounded border border-neutral-11">
        {editor()}
      </div>
      <button
        type="button"
        className="self-end text-xxs text-neutral-6 hover:text-neutral-3"
        onClick={() => {
          commit()
          setExpanded(true)
        }}
      >
        {t('shared.codeEditor.expand')}
      </button>
      {expanded && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-8"
          onClick={() => {
            commit()
            setExpanded(false)
          }}
        >
          <div
            className="flex h-full w-full max-w-4xl flex-col gap-2 rounded border border-neutral-10 bg-neutral-14 p-4"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between">
              <span className="text-xs font-bold uppercase text-neutral-5">
                {t('shared.codeEditor.title')}
              </span>
              <button
                type="button"
                className="text-xs text-neutral-5 hover:text-neutral-2"
                onClick={() => {
                  commit()
                  setExpanded(false)
                }}
              >
                {t('shared.codeEditor.close')}
              </button>
            </div>
            <div className="min-h-0 flex-1 overflow-auto rounded border border-neutral-11">
              {editor('100%')}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
