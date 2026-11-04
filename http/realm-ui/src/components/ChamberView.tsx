import { useState } from 'react';
import { Button, Label, Select, Spinner, TextInput } from 'flowbite-react';
import { HiClock, HiPencil, HiPlus, HiTrash } from 'react-icons/hi';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ConflictError, LoadedChamber, getCatalog, getChamber, getRevision, saveChamber } from '../api';
import { Chamber, Flag, FlagValue, RuleType } from '../models/response';
import { diffChambers, formatValue } from '../utils/describe';
import { ConfirmChange } from './ConfirmChange';
import { FlagEditor } from './FlagEditor/FlagEditor';
import { defaultValueFor } from '../utils/flags';
import { scheduleState } from '../utils/schedule';
import { HistoryDrawer } from './HistoryDrawer';
import { useSession } from '../session';

/** Attribute names suggested in the editor, in addition to catalog and in-use names. */
const CONVENTIONAL_ATTRIBUTES = [
  'key',
  'tenantId',
  'locationId',
  'environment',
  'service',
  'region',
  'version',
  'buildSha',
];

type Proposal = {
  title: string;
  apply: (chamber: Chamber) => Chamber;
  base: LoadedChamber;
  after: Chamber;
  saving: boolean;
  rebased?: boolean;
  error?: string;
};

const attributesIn = (chamber: Chamber | null) =>
  Object.values(chamber?.rules ?? {}).flatMap((f) =>
    (f.targeting ?? []).flatMap((r) => [...(r.clauses ?? []).map((c) => c.attribute), r.rollout?.bucketBy ?? ''])
  );

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

/** Rows share the table's column tracks so every column lines up. */
const ROW = 'sm:col-span-4 sm:grid sm:grid-cols-subgrid sm:items-center';

/** A list of flags with a column header; rows are FlagRows. */
const FlagTable = ({ children }: { children: React.ReactNode }) => (
  <div role="table" className="sm:grid sm:grid-cols-[minmax(0,5fr)_minmax(0,4fr)_minmax(0,3fr)_auto] sm:gap-x-4">
    <div
      role="row"
      className={`hidden border-b border-gray-200 px-4 py-2 text-xs font-medium text-gray-500 dark:border-gray-700 dark:text-gray-400 ${ROW}`}
    >
      <span role="columnheader">Flag</span>
      <span role="columnheader">Default</span>
      <span role="columnheader">Targeting</span>
      <span role="columnheader" className="sr-only">
        Actions
      </span>
    </div>
    <div role="rowgroup" className={`divide-y divide-gray-200 dark:divide-gray-700 ${ROW}`}>
      {children}
    </div>
  </div>
);

/** One flag: its key and type, the value it serves by default, and its targeting at a glance. */
const FlagRow = ({
  name,
  flag,
  muted,
  children,
}: {
  name: string;
  flag: Flag;
  muted?: boolean;
  children?: React.ReactNode;
}) => {
  const rules = flag.targeting ?? [];
  const rollouts = rules.filter((r) => r.rollout).length;
  const scheduled = rules.filter((r) => r.schedule && scheduleState(r.schedule) !== 'ended').length;
  return (
    <div
      role="row"
      className={`flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 ${ROW} ${muted ? 'text-gray-600 dark:text-gray-400' : ''}`}
    >
      <div role="cell" className="min-w-0 basis-full sm:basis-auto">
        <div className="truncate font-mono text-sm font-semibold" title={name}>
          {name}
        </div>
        <div className="text-xs text-gray-500 dark:text-gray-400">{flag.type}</div>
      </div>
      <div role="cell" className="min-w-0">
        <FlagValueView type={flag.type} value={flag.value} />
      </div>
      <div role="cell" className={`text-sm text-gray-600 dark:text-gray-400 ${rules.length ? '' : 'hidden sm:block'}`}>
        {rules.length === 0 ? (
          <span className="text-gray-400 dark:text-gray-500">None</span>
        ) : (
          <>
            {plural(rules.length, 'rule')}
            {rollouts > 0 && (
              <span className="block text-xs text-gray-500 dark:text-gray-400">
                {rollouts === rules.length ? 'gradual rollout' : `${rollouts} with gradual rollout`}
              </span>
            )}
            {scheduled > 0 && (
              <span className="block text-xs text-gray-500 dark:text-gray-400">
                {scheduled === rules.length ? 'scheduled' : `${scheduled} scheduled`}
              </span>
            )}
          </>
        )}
      </div>
      <div role="cell" className="ml-auto flex gap-1.5">
        {children}
      </div>
    </div>
  );
};

/** A served value: booleans as an on/off indicator, everything else as code. */
const FlagValueView = ({ type, value }: { type: RuleType; value: FlagValue }) => {
  if (type === 'boolean') {
    const on = value === true;
    return (
      <span className="inline-flex items-center gap-2 font-mono text-sm">
        <span
          aria-hidden="true"
          className={
            on
              ? 'h-2.5 w-2.5 rounded-full bg-primary-600 ring-2 ring-primary-100 dark:bg-primary-400 dark:ring-primary-900'
              : 'h-2.5 w-2.5 rounded-full border-2 border-gray-400 dark:border-gray-500'
          }
        />
        {on ? 'true' : 'false'}
      </span>
    );
  }
  const text = formatValue(value);
  return (
    <code
      className="inline-block max-w-full truncate rounded-md bg-gray-100 px-1.5 py-0.5 align-middle text-sm text-gray-800 dark:bg-gray-700 dark:text-gray-200"
      title={text}
    >
      {text}
    </code>
  );
};

export const ChamberView = ({ path }: { path: string }) => {
  const queryClient = useQueryClient();
  const { canEdit } = useSession();
  const { data, isLoading, error } = useQuery({ queryKey: ['chamber', path], queryFn: () => getChamber(path) });
  const { data: catalog = {} } = useQuery({ queryKey: ['catalog'], queryFn: getCatalog });
  const [editing, setEditing] = useState<{ name: string; flag: Flag; isNew: boolean } | null>(null);
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [newName, setNewName] = useState('');
  const [newType, setNewType] = useState<RuleType>('boolean');

  if (isLoading) return <Spinner aria-label="Loading chamber" size="lg" />;
  if (error instanceof Error) return <p className="text-red-700 dark:text-red-400">{error.message}</p>;
  if (!data?.own) {
    return (
      <p className="rounded-xl border border-dashed border-gray-300 px-4 py-6 text-sm text-gray-500 dark:border-gray-600 dark:text-gray-400">
        There is no chamber at this path yet.{canEdit && ' Use “New child chamber” to create one beneath it.'}
      </p>
    );
  }

  const own = data.own;
  const ownNames = Object.keys(own.rules).sort();
  const inherited = Object.entries(data.merged?.rules ?? {})
    .filter(([name]) => !(name in own.rules))
    .sort(([a], [b]) => a.localeCompare(b));
  const attributes = Array.from(
    new Set([...CONVENTIONAL_ATTRIBUTES, ...Object.keys(catalog), ...attributesIn(data.merged)].filter(Boolean))
  ).sort();

  const propose = (title: string, apply: Proposal['apply']) => {
    setProposal({ title, apply, base: data, after: apply(structuredClone(own)), saving: false });
  };

  const confirm = async () => {
    if (!proposal) return;
    setProposal({ ...proposal, saving: true, error: undefined });
    try {
      await saveChamber(path, proposal.after, proposal.base.etag);
      setProposal(null);
      setEditing(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['chamber', path] }),
        queryClient.invalidateQueries({ queryKey: ['history', path] }),
      ]);
    } catch (err) {
      if (err instanceof ConflictError) {
        // Re-apply the same change on top of the latest version and ask again.
        const fresh = await getChamber(path);
        queryClient.setQueryData(['chamber', path], fresh);
        const after = proposal.apply(structuredClone(fresh.own ?? { rules: {} }));
        setProposal({ ...proposal, base: fresh, after, saving: false, rebased: true });
        return;
      }
      setProposal({ ...proposal, saving: false, error: (err as Error).message });
    }
  };

  const restore = async (revision: number) => {
    const rev = await getRevision(path, revision);
    if (!rev.chamber) return;
    const chamber = rev.chamber;
    setHistoryOpen(false);
    propose(`Restore revision #${revision}`, () => structuredClone(chamber));
  };

  return (
    <div className="flex flex-col gap-8">
      <section aria-labelledby="flags-heading" className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-3">
          <h2 id="flags-heading" className="text-lg font-semibold">
            Flags <span className="font-normal text-gray-500 dark:text-gray-400">{ownNames.length}</span>
          </h2>
          <Button size="xs" color="light" onClick={() => setHistoryOpen(true)}>
            <HiClock className="mr-1" /> History
          </Button>
        </div>

        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800">
          {ownNames.length > 0 ? (
            <FlagTable>
              {ownNames.map((name) => {
                const flag = own.rules[name];
                return (
                  <FlagRow key={name} name={name} flag={flag}>
                    {canEdit && (
                      <>
                        <Button size="xs" color="light" onClick={() => setEditing({ name, flag, isNew: false })}>
                          <HiPencil className="mr-1" /> Edit
                        </Button>
                        <Button
                          size="xs"
                          color="light"
                          aria-label={`Delete ${name}`}
                          title={`Delete ${name}`}
                          className="text-gray-500 hover:text-red-700 dark:text-gray-400 dark:hover:text-red-400"
                          onClick={() =>
                            propose(`Delete ${name}`, (c) => {
                              delete c.rules[name];
                              return c;
                            })
                          }
                        >
                          <HiTrash />
                        </Button>
                      </>
                    )}
                  </FlagRow>
                );
              })}
            </FlagTable>
          ) : (
            <p className="px-4 py-6 text-sm text-gray-500 dark:text-gray-400">
              No flags in this chamber yet.{canEdit && ' Add one below, or override an inherited flag.'}
            </p>
          )}

          {canEdit && (
            <form
              className="grid grid-cols-1 items-end gap-3 border-t border-gray-200 bg-gray-50 p-4 sm:grid-cols-[2fr_1fr_auto] dark:border-gray-700 dark:bg-gray-800/60"
              onSubmit={(e) => {
                e.preventDefault();
                const name = newName.trim();
                if (!name || name in own.rules) return;
                setEditing({ name, flag: { type: newType, value: defaultValueFor(newType) }, isNew: true });
                setNewName('');
              }}
            >
              <div>
                <Label htmlFor="new-flag">New flag</Label>
                <TextInput
                  id="new-flag"
                  sizing="sm"
                  className="[&_input]:font-mono"
                  placeholder="checkout.newFlow"
                  value={newName}
                  color={newName.trim() in own.rules ? 'failure' : undefined}
                  onChange={(e) => setNewName(e.target.value)}
                />
              </div>
              <div>
                <Label htmlFor="new-flag-type">Type</Label>
                <Select
                  id="new-flag-type"
                  sizing="sm"
                  value={newType}
                  onChange={(e) => setNewType(e.target.value as RuleType)}
                >
                  <option value="boolean">boolean</option>
                  <option value="string">string</option>
                  <option value="number">number</option>
                  <option value="custom">custom (JSON)</option>
                </Select>
              </div>
              <Button type="submit" size="sm" disabled={!newName.trim() || newName.trim() in own.rules}>
                <HiPlus className="mr-1" /> Add flag
              </Button>
            </form>
          )}
        </div>
      </section>

      {inherited.length > 0 && (
        <section aria-labelledby="inherited-heading" className="flex flex-col gap-3">
          <div>
            <h2 id="inherited-heading" className="text-lg font-semibold">
              Inherited <span className="font-normal text-gray-500 dark:text-gray-400">{inherited.length}</span>
            </h2>
            <p className="text-sm text-gray-500 dark:text-gray-400">
              Served here from parent chambers until this chamber overrides them.
            </p>
          </div>
          <div className="overflow-hidden rounded-xl border border-dashed border-gray-300 bg-white/50 dark:border-gray-600 dark:bg-gray-800/40">
            <FlagTable>
              {inherited.map(([name, flag]) => (
                <FlagRow key={name} name={name} flag={flag} muted>
                  {canEdit && (
                    <Button size="xs" color="light" onClick={() => setEditing({ name, flag, isNew: true })}>
                      Override here
                    </Button>
                  )}
                </FlagRow>
              ))}
            </FlagTable>
          </div>
        </section>
      )}

      {editing && !proposal && (
        <FlagEditor
          name={editing.name}
          flag={editing.flag}
          catalog={catalog}
          attributes={attributes}
          onCancel={() => setEditing(null)}
          onReview={(flag) => {
            // Keep the draft so cancelling the confirmation returns to it.
            setEditing({ ...editing, flag });
            propose(`${editing.isNew ? 'Add' : 'Update'} ${editing.name}`, (c) => {
              c.rules[editing.name] = flag;
              return c;
            });
          }}
        />
      )}

      {proposal && (
        <ConfirmChange
          title={proposal.title}
          changes={diffChambers(proposal.base.own, proposal.after, catalog)}
          saving={proposal.saving}
          rebased={proposal.rebased}
          error={proposal.error}
          onCancel={() => setProposal(null)}
          onConfirm={() => void confirm()}
        />
      )}

      <HistoryDrawer
        path={path}
        open={historyOpen}
        onClose={() => setHistoryOpen(false)}
        onRestore={canEdit ? (revision) => void restore(revision) : undefined}
      />
    </div>
  );
};
