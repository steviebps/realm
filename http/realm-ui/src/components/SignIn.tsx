import { Button, Card } from 'flowbite-react';
import { FaGithub } from 'react-icons/fa';
import { signInURL } from '../api';
import { Provider } from '../models/response';
import { RealmMark } from './RealmMark';

/** Shown instead of the app when the server requires sign-in and nobody is signed in. */
export const SignIn = ({ providers }: { providers: Provider[] }) => (
  <div className="flex min-h-screen items-center justify-center px-4">
    <Card className="w-full max-w-sm">
      <div className="flex flex-col items-center gap-2 text-center">
        <RealmMark className="h-10 w-10 text-primary-700 dark:text-primary-400" />
        <h1 className="text-xl font-semibold">Sign in to realm</h1>
        <p className="text-sm text-gray-500 dark:text-gray-400">
          {providers.length > 0
            ? 'Use your work account to view and change flags.'
            : 'Sign-in is not configured on this server. Use the API with an API key instead.'}
        </p>
      </div>
      <div className="flex flex-col gap-2">
        {providers.map((p) => (
          <Button key={p.name} href={signInURL(p.name)} color={p.type === 'github' ? 'dark' : 'default'}>
            {p.type === 'github' && <FaGithub className="mr-2 h-4 w-4" />}
            Continue with {p.displayName}
          </Button>
        ))}
      </div>
    </Card>
  </div>
);
