export type Example = { context: string; sentence: string };
export type Sense = {
  part_of_speech: string;
  definition: string;
  when_to_use: string;
  examples: Example[];
  common_phrases: string[];
  usage_tip?: string;
};
export type Word = {
  id: string;
  term: string;
  original_sentence: string;
  source_url: string;
  personal_note: string;
  senses: Sense[];
  status: 'needs_details' | 'complete';
  revision: number;
  created_at: string;
  updated_at: string;
};
export type Capture = Pick<Word, 'term' | 'original_sentence' | 'source_url' | 'personal_note'>;
export type WordPage = { items: Word[]; total: number; page: number; page_size: number };
export type Session = { owner_id: string; label: string; csrf_token: string; scopes: string[] };
export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, { ...options, cache: 'no-store', credentials: 'same-origin' });
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error;
    throw new APIError(0, 'network', 'Could not connect. Your input has been kept. Please retry.');
  }
  const text = await response.text();
  let result: unknown;
  try {
    result = JSON.parse(text);
  } catch {
    result = null;
  }
  if (!response.ok) {
    const failure = result as { error?: string; code?: string } | null;
    throw new APIError(
      response.status,
      failure?.code || 'request_failed',
      failure?.error || text.trim() || 'Request failed. Please retry.',
    );
  }
  return result as T;
}
export function write<T>(path: string, method: string, input: unknown, csrf: string) {
  return api<T>(path, {
    method,
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: JSON.stringify(input),
  });
}
export function message(error: unknown): string {
  return error instanceof Error ? error.message : 'Request failed. Please retry.';
}
