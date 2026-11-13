import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import exec from 'k6/execution';

const base = (__ENV.BASE_URL || 'http://localhost:8090').replace(/\/$/, '');
const run = __ENV.RUN_ID;
const password = __ENV.LOADTEST_PASSWORD;
const includeAssistant = __ENV.INCLUDE_ASSISTANT === 'true';
if (!run || !/^[a-zA-Z0-9_]{1,12}$/.test(run)) throw new Error('RUN_ID must be 1-12 letters, digits or underscores');
if (!password || password.length < 8) throw new Error('Set LOADTEST_PASSWORD (at least 8 characters)');
if (__ENV.ALLOW_LOAD_TEST !== 'yes') throw new Error('Set ALLOW_LOAD_TEST=yes only for an isolated local-auth test environment');

export const options = {
  stages: [
    { duration: '1m', target: 100 }, { duration: '3m', target: 100 },
    { duration: '1m', target: 200 }, { duration: '3m', target: 200 },
    { duration: '1m', target: 0 },
  ],
  thresholds: { http_req_failed: ['rate<0.01'], checks: ['rate>0.99'] },
};
let userID;
function request(method, path, body, name) {
  const cookies = http.cookieJar().cookiesForURL(base);
  return http.request(method, base + path, body === undefined ? null : JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': (cookies.csrf_token || [''])[0] },
    tags: { name }, timeout: '120s',
  });
}
function expect(response, status, label) {
  if (!check(response, { [label]: r => r.status === status })) fail(`${label}: HTTP ${response.status}`);
}
export default function () {
  // A unique run ID prevents a previous run's seeded data from contaminating results.
  const username = `lt_${run}_${exec.vu.idInTest}`;
  if (!userID) {
    expect(request('POST', '/api/users/register', { username, password }, 'register'), 201, 'register');
  }
  const login = request('POST', '/api/users/login', { username, password }, 'login');
  expect(login, 200, 'login');
  userID = login.json('id');
  if (!userID) fail('login returned no user ID');
  const prefix = `/api/users/${userID}`;
  expect(request('PUT', prefix + '/profile', {
    monthly_disposable_cents: 150000, housing: 'on_campus', default_budget_cents: 100000,
  }, 'profile'), 200, 'profile');
  const date = new Date(Date.now() + 8 * 3600000).toISOString().slice(0, 10);
  expect(request('GET', prefix + `/dashboard?month=${date.slice(0, 7)}`, undefined, 'dashboard'), 200, 'dashboard');
  expect(request('POST', prefix + '/transactions', {
    amount_cents: 500, spent_on: date, category: 'food', merchant: 'loadtest',
  }, 'transaction'), 201, 'transaction');
  if (includeAssistant) {
    const answer = request('POST', prefix + '/assistant/ask', { question: 'Give me a budget overview.' }, 'assistant');
    expect(answer, 200, 'assistant HTTP');
    check(answer, { 'assistant completes SSE': r => /event:\s*done/.test(r.body || '') });
  }
  sleep(1);
}
export function handleSummary(data) {
  return { [`loadtest/results/${run}.summary.json`]: JSON.stringify(data, null, 2) };
}
