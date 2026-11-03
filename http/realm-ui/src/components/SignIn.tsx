import { Button, Card } from 'flowbite-react';
import { FaGithub } from 'react-icons/fa';
import { HiLockClosed } from 'react-icons/hi';
import { signInURL } from '../api';
import { Provider } from '../models/response';

/** Shown instead of the app when the server requires sign-in and nobody is signed in. */
export const SignIn = ({ providers }: { providers: Provider[] }) => (
  <div className="flex min-h-screen items-center justify-center px-4">
    <Card className="w-full max-w-sm">
      <div className="flex flex-col items-center gap-2 text-center">
        <HiLockClosed className="h-8 w-8 text-gray-500" />
        <h1 className="text-2xl font-bold">Sign in to Realm</h1>
        <p className="text-sm text-gray-500 dark:text-gray-400">Use your work account to view and change flags.</p>
      </div>
      <div className="flex flex-col gap-2">
        {providers.map((p) => (
          <Button key={p.name} href={signInURL(p.name)} color={p.type === 'github' ? 'dark' : 'blue'}>
            {p.type === 'github' && <FaGithub className="mr-2 h-4 w-4" />}
            Continue with {p.displayName}
          </Button>
        ))}
      </div>
    </Card>
  </div>
);
