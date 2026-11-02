import { Alert, Badge, Button, Modal, ModalBody, ModalFooter, ModalHeader, Spinner } from 'flowbite-react';
import { FlagChange } from '../utils/describe';

type Props = {
  title: string;
  changes: FlagChange[];
  saving: boolean;
  error?: string;
  /** Set when the chamber changed underneath and the diff was recomputed. */
  rebased?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
};

const kindColor = { added: 'success', removed: 'failure', changed: 'warning' } as const;

/** Shows exactly what a save will change and asks for confirmation. */
export const ConfirmChange = ({ title, changes, saving, error, rebased, onCancel, onConfirm }: Props) => (
  <Modal show size="3xl" onClose={saving ? () => undefined : onCancel}>
    <ModalHeader>{title}</ModalHeader>
    <ModalBody>
      <div className="flex flex-col gap-4">
        {rebased && (
          <Alert color="warning">
            Someone else changed this chamber while you were editing. Your change has been re-applied to the latest
            version — please review it again.
          </Alert>
        )}
        {error && <Alert color="failure">{error}</Alert>}
        {changes.length === 0 && <p className="text-gray-500">No changes.</p>}
        {changes.map((change) => (
          <section key={change.flag}>
            <h4 className="mb-1 flex items-center gap-2 font-semibold">
              {change.flag} <Badge color={kindColor[change.kind]}>{change.kind}</Badge>
            </h4>
            <ul className="rounded-lg border border-gray-200 font-mono text-sm dark:border-gray-700">
              {change.lines.map((line, i) => (
                <li
                  key={i}
                  className={
                    line.kind === 'add'
                      ? 'bg-green-50 px-3 py-1 text-green-900 dark:bg-green-900/30 dark:text-green-200'
                      : line.kind === 'remove'
                        ? 'bg-red-50 px-3 py-1 text-red-900 line-through decoration-red-400/60 dark:bg-red-900/30 dark:text-red-200'
                        : 'px-3 py-1 text-gray-600 dark:text-gray-300'
                  }
                >
                  {line.kind === 'add' ? '+ ' : line.kind === 'remove' ? '− ' : '  '}
                  {line.text}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </ModalBody>
    <ModalFooter className="justify-end">
      <Button color="light" onClick={onCancel} disabled={saving}>
        Cancel
      </Button>
      <Button onClick={onConfirm} disabled={saving || changes.length === 0}>
        {saving && <Spinner size="sm" className="mr-2" />}
        Confirm and save
      </Button>
    </ModalFooter>
  </Modal>
);
