import { initializeApp, type FirebaseApp } from "firebase/app";
import {
  getAuth,
  signInWithPopup,
  signInWithRedirect,
  GoogleAuthProvider,
  GithubAuthProvider,
  signInWithEmailAndPassword,
  createUserWithEmailAndPassword,
  signOut,
  onAuthStateChanged,
  type Auth,
  type User as FirebaseUser,
} from "firebase/auth";

const firebaseConfig = {
  apiKey: import.meta.env.VITE_FIREBASE_API_KEY,
  authDomain: import.meta.env.VITE_FIREBASE_AUTH_DOMAIN,
  projectId: import.meta.env.VITE_FIREBASE_PROJECT_ID,
};

// Lazy initialization — Firebase SDK throws if apiKey is missing/invalid,
// which breaks test environments that import this module transitively.
let _app: FirebaseApp | null = null;
let _auth: Auth | null = null;

function getFirebaseAuth(): Auth {
  if (!_auth) {
    if (!firebaseConfig.apiKey) {
      throw new Error("Firebase not configured — set VITE_FIREBASE_API_KEY");
    }
    _app = initializeApp(firebaseConfig);
    _auth = getAuth(_app);
  }
  return _auth;
}

const googleProvider = new GoogleAuthProvider();
const githubProvider = new GithubAuthProvider();

const REDIRECT_FLAG = "ocm_redirect_pending";

/**
 * Try popup first. If it fails (common on mobile), fall back to redirect.
 * Returns the ID token on popup success, or "" if redirecting away.
 */
async function signInWithProvider(provider: GoogleAuthProvider | GithubAuthProvider): Promise<string> {
  const auth = getFirebaseAuth();
  try {
    const result = await signInWithPopup(auth, provider);
    return result.user.getIdToken();
  } catch (err: unknown) {
    const code = (err as { code?: string })?.code;
    // Popup blocked or closed — fall back to redirect
    if (code === "auth/popup-blocked" || code === "auth/popup-closed-by-user" || code === "auth/cancelled-popup-request") {
      sessionStorage.setItem(REDIRECT_FLAG, "1");
      await signInWithRedirect(auth, provider);
      return "";
    }
    throw err;
  }
}

export async function signInWithGoogle(): Promise<string> {
  return signInWithProvider(googleProvider);
}

export async function signInWithGithub(): Promise<string> {
  return signInWithProvider(githubProvider);
}

/**
 * Wait for the Firebase user after a redirect sign-in (mobile OAuth).
 * Uses onAuthStateChanged which is more reliable than getRedirectResult
 * on mobile browsers. Returns the ID token, or null after timeout.
 */
export function waitForRedirectUser(timeoutMs = 10000): Promise<string | null> {
  return new Promise((resolve) => {
    const auth = getFirebaseAuth();
    const timer = setTimeout(() => {
      unsub();
      resolve(null);
    }, timeoutMs);

    const unsub = onAuthStateChanged(auth, async (user) => {
      if (user) {
        unsub();
        clearTimeout(timer);
        try {
          const token = await user.getIdToken();
          resolve(token);
        } catch {
          resolve(null);
        }
      }
      // If user is null, SDK is still initializing — keep waiting
    });
  });
}

export function isRedirectPending(): boolean {
  return sessionStorage.getItem(REDIRECT_FLAG) === "1";
}

export function clearRedirectFlag(): void {
  sessionStorage.removeItem(REDIRECT_FLAG);
}

export async function signInWithEmail(email: string, password: string): Promise<string> {
  const result = await signInWithEmailAndPassword(getFirebaseAuth(), email, password);
  return result.user.getIdToken();
}

export async function signUpWithEmail(email: string, password: string): Promise<string> {
  const result = await createUserWithEmailAndPassword(getFirebaseAuth(), email, password);
  return result.user.getIdToken();
}

export async function firebaseSignOut(): Promise<void> {
  if (!_auth) return; // Nothing to sign out of
  await signOut(_auth);
}

export function onFirebaseAuthChange(callback: (user: FirebaseUser | null) => void): () => void {
  return onAuthStateChanged(getFirebaseAuth(), callback);
}

export async function getFirebaseIdToken(): Promise<string | null> {
  if (!_auth) return null;
  const user = _auth.currentUser;
  if (!user) return null;
  return user.getIdToken();
}
