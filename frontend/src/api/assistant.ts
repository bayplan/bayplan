export interface AssistantMessage {
  role: 'user' | 'assistant'
  content: string
}

export type AssistantEvent =
  | { type: 'tool'; name: string; args: Record<string, unknown> }
  | { type: 'tool_error'; name: string; message: string }
  | { type: 'note'; text: string }
  | { type: 'answer'; text: string }
  | { type: 'done'; inputTokens: number; outputTokens: number; remainingToday: number; provider: string; model: string }
  | { type: 'error'; message: string }

function csrfToken(): string | undefined {
  return document.cookie
    .split('; ')
    .find((row) => row.startsWith('bayplan_csrf='))
    ?.split('=')[1]
}

/**
 * Sends the whole conversation (the server keeps none) and reads the NDJSON
 * answer as it arrives: fetch and not axios, which cannot stream a response
 * body in the browser.
 */
export async function chat(
  body: { messages: AssistantMessage[]; clusterId?: number; namespaces?: string[]; lang: string },
  onEvent: (e: AssistantEvent) => void,
  signal: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const csrf = csrfToken()
  if (csrf) headers['X-CSRF-Token'] = csrf
  const resp = await fetch('/api/assistant/chat', { method: 'POST', credentials: 'same-origin', headers, body: JSON.stringify(body), signal })
  if (!resp.ok || !resp.body) {
    const text = (await resp.text()).trim()
    throw new Error(text || `HTTP ${resp.status}`)
  }
  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    let nl = buffer.indexOf('\n')
    while (nl >= 0) {
      const line = buffer.slice(0, nl).trim()
      buffer = buffer.slice(nl + 1)
      if (line) onEvent(JSON.parse(line) as AssistantEvent)
      nl = buffer.indexOf('\n')
    }
  }
}
