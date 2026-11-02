import { useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  ClipboardWithIconText,
  Drawer,
  DrawerHeader,
  DrawerItems,
  Label,
  Select,
  Spinner,
  TextInput,
} from 'flowbite-react';
import { HiKey, HiTrash } from 'react-icons/hi';
import { useMutation, useQuery, useQueryClient } from 'react-query';
import { createKey, listKeys, revokeKey } from '../api';
import { CreatedKey, KeyAccess } from '../models/response';

type Props = {
  /** Suggested scope for new keys: the chamber being viewed. */
  path: string;
  open: boolean;
  onClose: () => void;
};

/** Lets administrators create and revoke API keys. */
export const KeysDrawer = ({ path, open, onClose }: Props) => {
  const queryClient = useQueryClient();
  const { data: keys, isLoading, error } = useQuery('keys', listKeys, { enabled: open });
  const [name, setName] = useState('');
  const [access, setAccess] = useState<KeyAccess>('read');
  const [scope, setScope] = useState(path);
  const [created, setCreated] = useState<CreatedKey | null>(null);

  const create = useMutation(createKey, {
    onSuccess: (result) => {
      setCreated(result);
      setName('');
      void queryClient.invalidateQueries('keys');
    },
  });
  const revoke = useMutation(revokeKey, {
    onSuccess: () => void queryClient.invalidateQueries('keys'),
  });

  const close = () => {
    setCreated(null);
    onClose();
  };

  return (
    <Drawer open={open} onClose={close} position="right" className="w-full max-w-lg">
      <DrawerHeader title="API keys" titleIcon={HiKey} />
      <DrawerItems className="flex flex-col gap-5">
        <p className="text-sm text-gray-500 dark:text-gray-400">
          Applications read flags with a <strong>read</strong> key; automation that changes flags needs a{' '}
          <strong>write</strong> key. A key only works for chambers at or beneath its path.
        </p>

        {created && (
          <Alert color="success" onDismiss={() => setCreated(null)}>
            <p className="mb-2 font-medium">
              Created “{created.key.name}”. Copy the token now — it won&apos;t be shown again.
            </p>
            <div className="relative">
              <TextInput readOnly value={created.token} className="font-mono" aria-label="New API key token" />
              <ClipboardWithIconText valueToCopy={created.token} />
            </div>
          </Alert>
        )}

        <form
          className="flex flex-col gap-3 rounded-lg border border-gray-200 p-3 dark:border-gray-700"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate({ name: name.trim(), access, path: scope });
          }}
        >
          <div>
            <Label htmlFor="key-name">Name</Label>
            <TextInput
              id="key-name"
              required
              maxLength={64}
              placeholder="checkout-service (production)"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="grid grid-cols-[1fr_2fr] gap-3">
            <div>
              <Label htmlFor="key-access">Access</Label>
              <Select id="key-access" value={access} onChange={(e) => setAccess(e.target.value as KeyAccess)}>
                <option value="read">read</option>
                <option value="write">write</option>
              </Select>
            </div>
            <div>
              <Label htmlFor="key-path">Chamber path</Label>
              <TextInput id="key-path" value={scope} onChange={(e) => setScope(e.target.value)} />
            </div>
          </div>
          {create.error instanceof Error && <Alert color="failure">{create.error.message}</Alert>}
          <Button type="submit" size="sm" disabled={!name.trim() || create.isLoading}>
            Create key
          </Button>
        </form>

        {isLoading && <Spinner aria-label="Loading keys" />}
        {error instanceof Error && <p className="text-red-600">{error.message}</p>}
        {revoke.error instanceof Error && <Alert color="failure">{revoke.error.message}</Alert>}
        {keys?.length === 0 && <p className="text-gray-500">No API keys yet.</p>}
        <ul className="flex flex-col gap-2">
          {keys?.map((key) => (
            <li
              key={key.id}
              className="flex items-center justify-between gap-2 rounded-lg border border-gray-200 p-2 dark:border-gray-700"
            >
              <div className="min-w-0 text-sm">
                <div className="flex items-center gap-2 font-medium">
                  <span className="truncate">{key.name}</span>
                  <Badge color={key.access === 'write' ? 'warning' : 'info'}>{key.access}</Badge>
                  <code className="text-xs text-gray-500">{key.path}</code>
                </div>
                <div className="text-gray-500">
                  {key.id} · created {new Date(key.createdAt).toLocaleDateString()} by {key.createdBy}
                </div>
              </div>
              <Button
                size="xs"
                color="red"
                outline
                disabled={revoke.isLoading}
                onClick={() => {
                  if (window.confirm(`Revoke “${key.name}”? Anything using it will lose access.`)) {
                    revoke.mutate(key.id);
                  }
                }}
              >
                <HiTrash className="mr-1" /> Revoke
              </Button>
            </li>
          ))}
        </ul>
      </DrawerItems>
    </Drawer>
  );
};
