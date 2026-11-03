import { Button, Drawer, DrawerHeader, DrawerItems, Spinner } from 'flowbite-react';
import { HiClock } from 'react-icons/hi';
import { useQuery } from '@tanstack/react-query';
import { getHistory } from '../api';

type Props = {
  path: string;
  open: boolean;
  onClose: () => void;
  /** Offers to restore earlier revisions; omitted for users who cannot edit. */
  onRestore?: (revision: number) => void;
};

const operationLabel = { put: 'saved', patch: 'patched', delete: 'deleted' } as const;

export const HistoryDrawer = ({ path, open, onClose, onRestore }: Props) => {
  const { data, isLoading, error } = useQuery({
    queryKey: ['history', path],
    queryFn: () => getHistory(path),
    enabled: open,
  });

  return (
    <Drawer open={open} onClose={onClose} position="right" className="w-full max-w-md">
      <DrawerHeader title="Change history" titleIcon={HiClock} />
      <DrawerItems>
        {isLoading && <Spinner />}
        {error instanceof Error && <p className="text-red-600">{error.message}</p>}
        {data?.length === 0 && <p className="text-gray-500">No changes recorded yet.</p>}
        <ol className="flex flex-col gap-2">
          {data?.map((rev, i) => (
            <li
              key={rev.revision}
              className="flex items-center justify-between gap-2 rounded-lg border border-gray-200 p-2 dark:border-gray-700"
            >
              <div className="text-sm">
                <div className="font-medium">
                  #{rev.revision} {operationLabel[rev.operation] ?? rev.operation} by {rev.actor}
                  {i === 0 && <span className="ml-1 text-gray-500">(current)</span>}
                </div>
                <time className="text-gray-500" dateTime={rev.time}>
                  {new Date(rev.time).toLocaleString()}
                </time>
              </div>
              {onRestore && i > 0 && rev.operation !== 'delete' && (
                <Button size="xs" color="light" onClick={() => onRestore(rev.revision)}>
                  Restore…
                </Button>
              )}
            </li>
          ))}
        </ol>
      </DrawerItems>
    </Drawer>
  );
};
