// A minimal `fetch`-compatible shim backed by Node's core `http`/`https`
// modules so the §6.3 suite can mock the admin API with **nock**.
//
// Why this exists: the AdminClient (admin-client.ts) talks to the engine via
// native `fetch` (undici) in production, and accepts a `fetchImpl` injection
// point precisely so a test can swap the transport. nock 13.x intercepts the
// Node `http`/`https` client but NOT undici's native `fetch`, so the suite
// injects THIS shim — a thin fetch over `http.request` — and nock then sees and
// asserts every request (method, path, headers, body) exactly as specified.
//
// The shim implements only the surface AdminClient uses: method, headers, a
// string body, and a Response exposing `ok`, `status`, and `text()`.

import http from 'node:http';
import https from 'node:https';

type HeadersInit = Record<string, string>;

interface MinimalRequestInit {
  method?: string;
  headers?: HeadersInit;
  body?: string;
}

interface MinimalResponse {
  ok: boolean;
  status: number;
  text(): Promise<string>;
  json(): Promise<unknown>;
}

export const httpFetch: typeof fetch = ((
  input: string | URL,
  init: MinimalRequestInit = {}
): Promise<MinimalResponse> => {
  const url = new URL(typeof input === 'string' ? input : input.toString());
  const transport = url.protocol === 'https:' ? https : http;

  return new Promise<MinimalResponse>((resolve, reject) => {
    const req = transport.request(
      {
        protocol: url.protocol,
        hostname: url.hostname,
        port: url.port || (url.protocol === 'https:' ? 443 : 80),
        path: `${url.pathname}${url.search}`,
        method: init.method ?? 'GET',
        headers: init.headers ?? {},
      },
      (res) => {
        const chunks: Buffer[] = [];
        res.on('data', (c) => chunks.push(Buffer.from(c)));
        res.on('end', () => {
          const text = Buffer.concat(chunks).toString('utf8');
          const status = res.statusCode ?? 0;
          resolve({
            ok: status >= 200 && status < 300,
            status,
            text: async () => text,
            json: async () => JSON.parse(text),
          });
        });
      }
    );
    req.on('error', reject);
    if (init.body != null) req.write(init.body);
    req.end();
  });
}) as unknown as typeof fetch;
