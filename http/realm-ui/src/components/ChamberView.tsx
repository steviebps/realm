import { useCallback, useMemo, useState } from 'react';
import { Alert, Button, Label, Select, Spinner, TextInput } from 'flowbite-react';
import { HiClock, HiExclamation, HiPencil, HiPlus, HiTrash } from 'react-icons/hi';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ConflictError, LoadedChamber, getCatalog, getChamber, getRevision, saveChamber } from '../api';
import { Chamber, Flag, RuleType } from '../models/response';
import { diffChambers } from '../utils/describe';
import { ConfirmChange } from './ConfirmChange';
import { FlagEditor } from './FlagEditor/FlagEditor';
import { defaultValueFor } from '../utils/flags';
import { ancestorsOf, get, provenance } from '../utils/provenance';
import { messageOf } from '../utils/strings';
import { HistoryDrawer } from './HistoryDrawer';
import { FlagRow, FlagTable } from './FlagTable';
import { InheritedSection } from './InheritedSection';
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

const attributesIn = (flags: Flag[]) =>
  flags.flatMap((f) =>
    (f.targeting ?? []).flatMap((r) => [...(r.clauses ?? []).map((c) => c.attribute), r.rollout?.bucketBy ?? ''])
  );

export const ChamberView = ({ path }: { path: string }) => {
  const queryClient = useQueryClient();
  const { canEdit } = useSession();
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['chamber', path],
    queryFn: () => getChamber(path),
  });
  const { data: catalog = {} } = useQuery({ queryKey: ['catalog'], queryFn: getCatalog });
  const [editing, setEditing] = useState<{ name: string; flag: Flag; isNew: boolean } | null>(null);
  const [proposal, setProposal] = useState<Proposal | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [restoreError, setRestoreError] = useState<string | undefined>();
  const [newName, setNewName] = useState('');
  const [newType, setNewType] = useState<RuleType>('boolean');
  // lineage is only set once its shape is checked, so layers[0] exists. Memoized so that typing in the
  // form doesn't hand every flag row new provenance objects.
  const lineage = data?.lineage ?? null;
  const prov = useMemo(() => (lineage ? provenance(lineage) : null), [lineage]);
  const override = useCallback((name: string, flag: Flag) => setEditing({ name, flag, isNew: true }), []);

  if (isLoading) return <Spinner aria-label="Loading chamber" size="lg" />;
  if (error instanceof Error) return <p className="text-red-700 dark:text-red-400">{error.message}</p>;
  if (!data?.own) {
    return (
      <p className="rounded-xl border border-dashed border-gray-300 px-4 py-6 text-sm text-gray-600 dark:border-gray-600 dark:text-gray-400">
        There is no chamber at this path yet.{canEdit && ' Use “New child chamber” to create one beneath it.'}
      </p>
    );
  }

  const own = data.own;
  const ownNames = Object.keys(own.rules).sort();
  const here = lineage?.layers[0].path ?? path;
  const inheritedFlags = Object.values(prov?.inherited ?? {}).map((s) => s.flag);
  const attributes = Array.from(
    new Set(
      [
        ...CONVENTIONAL_ATTRIBUTES,
        ...Object.keys(catalog),
        ...attributesIn([...Object.values(own.rules), ...inheritedFlags]),
      ].filter(Boolean)
    )
  ).sort();
  const typed = newName.trim();
  const typedInherited = typed in own.rules ? undefined : get(prov?.inherited, typed);
  // a parent that seals the typed key: this chamber cannot define it
  const typedSealedBy = typed in own.rules ? undefined : get(prov?.sealedBy, typed);
  const cannotAdd = !typed || typed in own.rules || typedSealedBy !== undefined;

  const propose = (title: string, apply: Proposal['apply']) => {
    setProposal({ title, apply, base: data, after: apply(structuredClone(own)), saving: false });
  };

  const confirm = async () => {
    if (!proposal) return;
    setProposal({ ...proposal, saving: true, error: undefined });
    try {
      await saveChamber(path, proposal.after, proposal.base.etag);
    } catch (err) {
      if (!(err instanceof ConflictError)) {
        setProposal({ ...proposal, saving: false, error: messageOf(err) });
        return;
      }
      // Re-apply the same change on top of the latest version and ask again.
      try {
        const fresh = await getChamber(path);
        queryClient.setQueryData(['chamber', path], fresh);
        const after = proposal.apply(structuredClone(fresh.own ?? { rules: {} }));
        setProposal({ ...proposal, base: fresh, after, saving: false, rebased: true });
      } catch (reloadErr) {
        // Keep the old base: confirming again retries the save, and the reload after it.
        setProposal({
          ...proposal,
          saving: false,
          error: `Someone else changed this chamber, and loading their change failed: ${messageOf(reloadErr)}`,
        });
      }
      return;
    }
    setProposal(null);
    setEditing(null);
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ['chamber', path] }),
      queryClient.invalidateQueries({ queryKey: ['history', path] }),
    ]);
  };

  const restore = async (revision: number) => {
    setRestoreError(undefined);
    try {
      const rev = await getRevision(path, revision);
      if (!rev.chamber) {
        setRestoreError(`Revision #${revision} has no chamber to restore.`);
        return;
      }
      const chamber = rev.chamber;
      setHistoryOpen(false);
      propose(`Restore revision #${revision}`, () => structuredClone(chamber));
    } catch (err) {
      setRestoreError(`Could not load revision #${revision}: ${messageOf(err)}`);
    }
  };

  return (
    <div className="flex flex-col gap-8">
      {lineage?.error !== undefined && (
        <Alert color="failure" icon={HiExclamation}>
          <span className="font-semibold">Clients can&rsquo;t load this chamber.</span> The server can&rsquo;t build the
          flags it serves: {lineage.error}. Running SDKs keep their last good copy. SDKs that start now, and evaluation
          requests for this path, fail until this is fixed.
        </Alert>
      )}
      <section aria-labelledby="flags-heading" className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-3">
          <h2 id="flags-heading" className="text-lg font-semibold">
            Flags <span className="font-normal text-gray-600 dark:text-gray-400">{ownNames.length}</span>
          </h2>
          <Button
            size="xs"
            color="light"
            onClick={() => {
              setRestoreError(undefined);
              setHistoryOpen(true);
            }}
          >
            <HiClock className="mr-1" /> History
          </Button>
        </div>

        <div className="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800">
          {ownNames.length > 0 ? (
            <FlagTable labelledBy="flags-heading">
              {ownNames.map((name) => {
                const flag = own.rules[name];
                // a definition saved before a parent sealed the flag: not served, and the server refuses changes
                const hiddenBy = get(prov?.sealedBy, name);
                return (
                  <FlagRow
                    key={name}
                    name={name}
                    flag={flag}
                    served={get(prov?.own, name)}
                    here={here}
                    catalog={catalog}
                    muted={hiddenBy !== undefined}
                    hiddenBy={hiddenBy}
                    actions={
                      canEdit && (
                        <>
                          {hiddenBy === undefined && (
                            <Button
                              size="xs"
                              color="light"
                              aria-label={`Edit ${name}`}
                              onClick={() => setEditing({ name, flag, isNew: false })}
                            >
                              <HiPencil className="mr-1" /> Edit
                            </Button>
                          )}
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
                      )
                    }
                  />
                );
              })}
            </FlagTable>
          ) : (
            <p className="px-4 py-6 text-sm text-gray-500 dark:text-gray-400">
              No flags in this chamber yet.{canEdit && ' Add one below, or open an inherited flag to override it.'}
            </p>
          )}

          {canEdit && (
            <form
              className="grid grid-cols-1 items-end gap-3 border-t border-gray-200 bg-gray-50 p-4 sm:grid-cols-[2fr_1fr_auto] dark:border-gray-700 dark:bg-gray-800/60"
              onSubmit={(e) => {
                e.preventDefault();
                const name = newName.trim();
                if (cannotAdd) return;
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
                  color={typed in own.rules || typedSealedBy !== undefined ? 'failure' : undefined}
                  aria-describedby={typedInherited || typedSealedBy !== undefined ? 'new-flag-hint' : undefined}
                  onChange={(e) => setNewName(e.target.value)}
                />
                {typedSealedBy !== undefined ? (
                  <p id="new-flag-hint" className="mt-1 text-xs text-red-700 dark:text-red-400">
                    <span className="font-mono">{typed}</span> is sealed by{' '}
                    <span className="font-mono">{typedSealedBy}</span>, so chambers below it can&rsquo;t define it.
                  </p>
                ) : (
                  typedInherited && (
                    <p id="new-flag-hint" className="mt-1 text-xs text-gray-600 dark:text-gray-400">
                      <span className="font-mono">{typed}</span> is inherited from{' '}
                      <span className="font-mono">{typedInherited.source}</span> ({typedInherited.flag.type}). Adding it
                      here overrides that definition.
                    </p>
                  )
                )}
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
              <Button type="submit" size="sm" disabled={cannotAdd}>
                <HiPlus className="mr-1" /> Add flag
              </Button>
            </form>
          )}
        </div>
      </section>

      {ancestorsOf(here).length > 0 && (
        <InheritedSection
          here={here}
          prov={prov}
          loadError={data.lineageError}
          onRetry={refetch}
          catalog={catalog}
          onOverride={canEdit ? override : undefined}
        />
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
        error={restoreError}
        onRestore={canEdit ? (revision) => void restore(revision) : undefined}
      />
    </div>
  );
};
