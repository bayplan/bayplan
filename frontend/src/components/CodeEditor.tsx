import { useEffect, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { yaml } from '@codemirror/lang-yaml'
import { EditorView } from '@codemirror/view'

interface CodeEditorProps {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  readOnly?: boolean
  className?: string
}

const yamlExtensions = [yaml(), EditorView.lineWrapping]

/**
 * YAML editor with highlighting, line numbers and indentation (CodeMirror 6).
 * Replaces the textarea we had: it adds ~300 KB to the bundle, but a 200-line
 * manifest without highlighting or line numbers is where most indentation
 * mistakes slip in, and the dry-run reports them as "error on line 143". The
 * theme follows the `.dark` class of <html> (lib/theme.ts) by observing its
 * changes: CodeMirror does not read CSS variables.

 */
export function CodeEditor({ value, onChange, placeholder, readOnly, className = '' }: CodeEditorProps) {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  useEffect(() => {
    const obs = new MutationObserver(() => setDark(document.documentElement.classList.contains('dark')))
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
    return () => obs.disconnect()
  }, [])
  return (
    <CodeMirror
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      readOnly={readOnly}
      editable={!readOnly}
      theme={dark ? 'dark' : 'light'}
      extensions={yamlExtensions}
      height="100%"
      className={`min-h-0 flex-1 overflow-hidden font-mono text-xs [&_.cm-editor]:h-full [&_.cm-scroller]:overflow-auto ${className}`}
      basicSetup={{ foldGutter: true, highlightActiveLine: true, autocompletion: false, searchKeymap: true }}
    />
  )
}
