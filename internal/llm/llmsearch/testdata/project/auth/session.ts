// Sessions: signed tokens that expire after a period of inactivity.

export interface Session {
  user: string;
  token: string;
  expiresAt: number;
}

const IDLE_LIMIT_MS = 30 * 60 * 1000;

export function startSession(user: string, sign: (s: string) => string): Session {
  const expiresAt = Date.now() + IDLE_LIMIT_MS;
  return { user, token: sign(`${user}:${expiresAt}`), expiresAt };
}

export function isExpired(session: Session, now: number = Date.now()): boolean {
  return now >= session.expiresAt;
}

export function refresh(session: Session): Session {
  return { ...session, expiresAt: Date.now() + IDLE_LIMIT_MS };
}
