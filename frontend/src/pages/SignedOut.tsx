import { Link } from "react-router-dom";
import { LogOut } from "lucide-react";

export function SignedOut() {
  return (
    <div className="min-h-screen bg-gray-950 flex items-center justify-center px-4">
      <div className="max-w-sm text-center">
        <div className="w-16 h-16 rounded-full bg-gray-800 flex items-center justify-center mx-auto mb-6">
          <LogOut className="w-7 h-7 text-gray-400" />
        </div>
        <h1 className="text-2xl font-bold text-white mb-2">You've been signed out</h1>
        <p className="text-gray-400 text-sm mb-8">
          Your session has been ended. To fully sign out, also sign out of your
          Google or GitHub account.
        </p>
        <div className="flex flex-col gap-3">
          <Link
            to="/login"
            className="rounded-lg bg-orange-500 px-6 py-3 font-semibold text-white hover:bg-orange-600 transition-colors"
          >
            Sign in again
          </Link>
          <Link
            to="/"
            className="text-sm text-gray-500 hover:text-gray-300 transition-colors"
          >
            Back to home
          </Link>
        </div>
      </div>
    </div>
  );
}
