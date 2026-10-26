/**
 * Copy-paste examples for the customer API journey: authenticate with a
 * scoped key, list nodes, create a lease and read usage. They are generated
 * from the console's resolved base URL so an example always targets the
 * daemon the console is reading.
 *
 * `secret` exists only for the one-time create dialog: it is substituted into
 * the text in memory and never written to storage. Everywhere else the
 * snippets read `AJS_API_KEY` from the environment.
 */

export type SnippetLanguage = 'curl' | 'go' | 'ts'

export interface SnippetContext {
  baseUrl: string
  /** The plaintext key, shown once after creation. Never persisted. */
  secret?: string
}

const PLACEHOLDER_SECRET = 'ajs_sk_...'
const USER_PLACEHOLDER = '<user-id>'
const ACCOUNT_PLACEHOLDER = '<account-id>'

function curlSnippet({ baseUrl, secret }: SnippetContext): string {
  const key = secret ?? PLACEHOLDER_SECRET
  return `export AJS_API_BASE="${baseUrl}"
export AJS_API_KEY="${key}"

# 1. Nodes this key can reach
curl -sS "$AJS_API_BASE/v1/nodes" \\
  -H "Authorization: Bearer $AJS_API_KEY"

# 2. Create a lease (platform: android | ios)
curl -sS -X POST "$AJS_API_BASE/v1/leases" \\
  -H "Authorization: Bearer $AJS_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"userId":"${USER_PLACEHOLDER}","platform":"android","accountId":"${ACCOUNT_PLACEHOLDER}","ttlSeconds":3600}'

# 3. Read that user's usage
curl -sS "$AJS_API_BASE/v1/usage?userId=${USER_PLACEHOLDER}" \\
  -H "Authorization: Bearer $AJS_API_KEY"`
}

function goSnippet({ baseUrl, secret }: SnippetContext): string {
  const key = secret ? `"${secret}"` : 'os.Getenv("AJS_API_KEY")'
  const osImport = secret ? '' : '\n\t"os"'
  return `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"${osImport}
)

func main() {
	const base = "${baseUrl}"
	key := ${key}

	call := func(method, path string, body any) {
		var payload io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			payload = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, base+path, payload)
		if err != nil {
			panic(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		fmt.Printf("%s %s -> %d %s\\n", method, path, resp.StatusCode, out)
	}

	call("GET", "/v1/nodes", nil)
	call("POST", "/v1/leases", map[string]any{
		"userId": "${USER_PLACEHOLDER}", "platform": "android",
		"accountId": "${ACCOUNT_PLACEHOLDER}", "ttlSeconds": 3600,
	})
	call("GET", "/v1/usage?userId=${USER_PLACEHOLDER}", nil)
}`
}

function tsSnippet({ baseUrl, secret }: SnippetContext): string {
  const key = secret ? `'${secret}'` : "process.env.AJS_API_KEY ?? ''"
  return `const base = '${baseUrl}'
const key = ${key}

async function call<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(base + path, {
    ...init,
    headers: {
      Authorization: \`Bearer \${key}\`,
      'Content-Type': 'application/json',
      ...init.headers,
    },
  })
  if (!res.ok) throw new Error(\`\${res.status} \${await res.text()}\`)
  return (await res.json()) as T
}

const nodes = await call('/v1/nodes')
const lease = await call('/v1/leases', {
  method: 'POST',
  body: JSON.stringify({
    userId: '${USER_PLACEHOLDER}',
    platform: 'android',
    accountId: '${ACCOUNT_PLACEHOLDER}',
    ttlSeconds: 3600,
  }),
})
const usage = await call('/v1/usage?userId=${USER_PLACEHOLDER}')
console.log({ nodes, lease, usage })`
}

export function apiSnippet(language: SnippetLanguage, context: SnippetContext): string {
  switch (language) {
    case 'go':
      return goSnippet(context)
    case 'ts':
      return tsSnippet(context)
    default:
      return curlSnippet(context)
  }
}
