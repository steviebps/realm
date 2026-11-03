import { useState } from 'react';
import { Badge, Button, Card, Label, Select, Spinner, TextInput } from 'flowbite-react';
import { HiClock, HiPencil, HiPlus, HiTrash } from 'react-icons/hi';
import { useQuery, useQueryClient } from 'react-query';
import { ConflictError, LoadedChamber, getCatalog, getChamber, getRevision, saveChamber } from '../api';
import { Chamber, Flag, RuleType } from '../models/response';
import { diffChambers, formatValue } from '../utils/describe';
import { ConfirmChange } from './ConfirmChange';
import { FlagEditor } from './FlagEditor/FlagEditor';
import { defaultValueFor } from '../utils/flags';
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

const summarize = (flag: Flag) => {
  const n = flag.targeting?.length ?? 0;
  return `Default ${formatValue(flag.value)}${n ? ` · ${n} rule${n === 1 ? '' : 's'}` : ''}`;
};

export const ChamberView = ({ path }: { path: string }) => {
  const queryClient = useQueryClient();
  const { canEdit } = useSession();
  const { data, isLoading, error } = useQuery(['chamber', path], () => getChamber(path));
  const { data: catalog = {} } = useQuery('catalog', getCatalog);
  const [editing, setEditing] = useState<{ name: string; flag: Flag; isNew: boolean } | null>(null);
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [newName, setNewName] = useState('');
  const [newType, setNewType] = useState<RuleType>('boolean');

  if (isLoading) return <Spinner aria-label="Loading chamber" size="lg" />;
  if (error instanceof Error) return <p className="text-red-600">{error.message}</p>;
  if (!data?.own) return <p className="text-gray-500">There is no chamber at this path yet.</p>;

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
        queryClient.invalidateQueries(['chamber', path]),
        queryClient.invalidateQueries(['history', path]),
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
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold">Flags</h2>
        <Button size="sm" color="light" onClick={() => setHistoryOpen(true)}>
          <HiClock className="mr-1" /> History
        </Button>
      </div>

      {ownNames.length === 0 && <p className="text-gray-500">No flags defined in this chamber yet.</p>}
      <ul className="grid list-none gap-3">
        {ownNames.map((name) => {
          const flag = own.rules[name];
          return (
            <li key={name}>
              <Card className="[&>div]:p-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div>
                    <div className="flex items-center gap-2 font-semibold">
                      {name} <Badge color="gray">{flag.type}</Badge>
                    </div>
                    <p className="text-sm text-gray-500 dark:text-gray-400">{summarize(flag)}</p>
                  </div>
                  {canEdit && (
                    <div className="flex gap-2">
                      <Button size="xs" onClick={() => setEditing({ name, flag, isNew: false })}>
                        <HiPencil className="mr-1" /> Edit
                      </Button>
                      <Button
                        size="xs"
                        color="red"
                        outline
                        onClick={() =>
                          propose(`Delete ${name}`, (c) => {
                            delete c.rules[name];
                            return c;
                          })
                        }
                      >
                        <HiTrash className="mr-1" /> Delete
                      </Button>
                    </div>
                  )}
                </div>
              </Card>
            </li>
          );
        })}
      </ul>

      {inherited.length > 0 && (
        <section>
          <h3 className="mb-2 font-semibold">Inherited from parent chambers</h3>
          <ul className="grid list-none gap-2">
            {inherited.map(([name, flag]) => (
              <li
                key={name}
                className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed border-gray-300 p-3 dark:border-gray-600"
              >
                <div>
                  <div className="flex items-center gap-2 font-medium">
                    {name} <Badge color="gray">{flag.type}</Badge> <Badge color="purple">inherited</Badge>
                  </div>
                  <p className="text-sm text-gray-500">{summarize(flag)}</p>
                </div>
                {canEdit && (
                  <Button size="xs" color="light" onClick={() => setEditing({ name, flag, isNew: true })}>
                    Override here
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      {canEdit && (
        <form
          className="grid grid-cols-1 items-end gap-3 sm:grid-cols-[2fr_1fr_auto]"
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
              placeholder="checkout.newFlow"
              value={newName}
              color={newName.trim() in own.rules ? 'failure' : undefined}
              onChange={(e) => setNewName(e.target.value)}
            />
          </div>
          <div>
            <Label htmlFor="new-flag-type">Type</Label>
            <Select id="new-flag-type" value={newType} onChange={(e) => setNewType(e.target.value as RuleType)}>
              <option value="boolean">boolean</option>
              <option value="string">string</option>
              <option value="number">number</option>
              <option value="custom">custom (JSON)</option>
            </Select>
          </div>
          <Button type="submit" disabled={!newName.trim() || newName.trim() in own.rules}>
            <HiPlus className="mr-1" /> Add flag
          </Button>
        </form>
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
