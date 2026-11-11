import { useState } from 'react';
import { Alert, Badge, Button } from 'flowbite-react';
import { HiLockClosed } from 'react-icons/hi';
import { Catalog, Flag } from '../models/response';
import { Provenance, Served, StackEntry } from '../utils/provenance';
import { plural } from '../utils/strings';
import { ChamberRef, FlagRow, FlagTable, Mono, PathLink, Sep } from './FlagTable';

type LayerEntry = Extract<StackEntry, { kind: 'layer' }>;

type Props = {
  /** The path of the chamber being viewed. */
  here: string;
  /** Null when the lineage could not be loaded. */
  prov: Provenance | null;
  /** Why the lineage could not be loaded. */
  loadError: string | null;
  onRetry: () => Promise<unknown>;
  catalog: Catalog;
  /** Starts overriding an inherited flag in this chamber; omitted for users who cannot edit. */
  onOverride?: (name: string, flag: Flag) => void;
};

const distanceLabel = (distance: number) => (distance === 1 ? 'parent' : `${distance} levels up`);

/** A parent chamber gets a card when it serves flags here (never when the view is broken: then nothing is served). */
const isCard = (entry: StackEntry): entry is LayerEntry => entry.kind === 'layer' && entry.served.length > 0;

/** Flag keys in monospace, separated by commas, each with the chamber that overrides it. */
const OverriddenList = ({ items, here }: { items: { name: string; by: string }[]; here: string }) => (
  <>
    {items.map(({ name, by }, i) => (
      <span key={name}>
        {i > 0 && ', '}
        <Mono>{name}</Mono> (by <ChamberRef path={by} here={here} />)
      </span>
    ))}
  </>
);

/** Flag keys in monospace, separated by commas, each with the chamber that seals it. */
const SealedList = ({ items }: { items: { name: string; by: string }[] }) => (
  <>
    {items.map(({ name, by }, i) => (
      <span key={name}>
        {i > 0 && ', '}
        <Mono>{name}</Mono> (sealed by <Mono>{by}</Mono>)
      </span>
    ))}
  </>
);

/** A parent chamber that contributes no flags here, and why, on one line. */
const StackLine = ({ entry, here, broken }: { entry: StackEntry; here: string; broken: boolean }) => {
  let label = 'not inherited';
  let sentence: React.ReactNode = <>Flags defined in {entry.path} are not served here.</>;
  let tone = '';
  if (entry.kind === 'layer') {
    label = distanceLabel(entry.distance);
    if (entry.status === 'missing') {
      sentence = 'No chamber at this path, so nothing is inherited from it.';
    } else if (entry.status === 'invalid') {
      sentence = `Can't be read: ${entry.error ?? 'unknown error'}`;
      tone = 'text-red-700 dark:text-red-400';
    } else if (entry.defined === 0) {
      sentence = 'Has no flags, so nothing is inherited from it.';
    } else if (broken) {
      sentence = `Defines ${plural(entry.defined, 'flag')}.`;
    } else if (entry.hidden.length === 0) {
      sentence = (
        <>
          Nothing is inherited from it. Every flag it defines is overridden nearer:{' '}
          <OverriddenList items={entry.overridden} here={here} />.
        </>
      );
    } else {
      sentence = (
        <>
          Nothing is inherited from it.
          {entry.overridden.length > 0 && (
            <>
              {' '}
              Overridden nearer: <OverriddenList items={entry.overridden} here={here} />.
            </>
          )}{' '}
          Hidden by a seal: <SealedList items={entry.hidden} />.
        </>
      );
    }
  }
  return (
    <li className="border-t border-dashed border-gray-300 pt-3 text-sm text-gray-600 first:border-t-0 first:pt-0 dark:border-gray-600 dark:text-gray-400">
      <PathLink path={entry.path} />
      <Sep />
      {label}
      <Sep symbol="—" />
      <span className={tone}>{sentence}</span>
    </li>
  );
};

/** A parent chamber that serves flags here: those flags, read only, and the ones it defines that are overridden nearer. */
const LayerCard = ({
  entry,
  here,
  catalog,
  onOverride,
}: {
  entry: LayerEntry;
  here: string;
  catalog: Catalog;
  onOverride?: Props['onOverride'];
}) => {
  const detailActions = (served: Served) => (
    <div className="flex flex-col gap-2">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-4">
        <PathLink path={served.source}>Open </PathLink>
        {onOverride && !served.sealedBy && (
          <Button
            size="xs"
            color="light"
            className="w-full sm:w-auto"
            onClick={() => onOverride(served.name, served.flag)}
          >
            Override in this chamber…
          </Button>
        )}
      </div>
      {served.sealedBy ? (
        <p className="text-xs text-gray-600 dark:text-gray-400">
          <Mono>{served.sealedBy}</Mono> seals <Mono>{served.name}</Mono>, so this chamber can&rsquo;t override it. To
          change it, edit it in <Mono>{served.sealedBy}</Mono>.
        </p>
      ) : (
        onOverride && (
          <p className="text-xs text-gray-600 dark:text-gray-400">
            Copies this definition into <Mono>{here}</Mono> for editing. After you save, changes to{' '}
            <Mono>{served.name}</Mono> in <Mono>{served.source}</Mono> no longer reach this chamber.
          </p>
        )
      )}
    </div>
  );
  return (
    <li className="overflow-hidden rounded-xl border border-dashed border-gray-300 bg-white/50 dark:border-gray-600 dark:bg-gray-800/40">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 px-4 pt-3 pb-1">
        <h3 className="text-sm font-semibold">
          From <PathLink path={entry.path} />
          <span className="font-normal text-gray-600 dark:text-gray-400">
            <Sep />
            {distanceLabel(entry.distance)}
          </span>
        </h3>
        <span className="text-xs text-gray-600 dark:text-gray-400">{entry.served.length} served here</span>
      </div>
      <FlagTable label={`Flags inherited from ${entry.path}`} headerHidden>
        {entry.served.map((served) => (
          <FlagRow
            key={served.name}
            name={served.name}
            flag={served.flag}
            served={served}
            here={here}
            catalog={catalog}
            muted
            detailActions={detailActions(served)}
          />
        ))}
      </FlagTable>
      {entry.overridden.length > 0 && (
        <p className="border-t border-dashed border-gray-300 px-4 py-2 text-xs text-gray-600 dark:border-gray-600 dark:text-gray-400">
          Also defines, overridden nearer: <OverriddenList items={entry.overridden} here={here} />
        </p>
      )}
      {entry.hidden.length > 0 && (
        <p className="border-t border-dashed border-gray-300 px-4 py-2 text-xs text-gray-600 dark:border-gray-600 dark:text-gray-400">
          Also defines, hidden by a seal: <SealedList items={entry.hidden} />
        </p>
      )}
    </li>
  );
};

/** Retries loading the lineage, disabled while it runs. */
const RetryButton = ({ onRetry }: { onRetry: Props['onRetry'] }) => {
  const [retrying, setRetrying] = useState(false);
  return (
    <Button
      size="xs"
      color="light"
      disabled={retrying}
      onClick={() => {
        setRetrying(true);
        void onRetry().finally(() => setRetrying(false));
      }}
    >
      Retry
    </Button>
  );
};

/**
 * The flags a chamber inherits, read only, grouped by the parent chamber that
 * serves each one, nearest first, with a line for each ancestor that
 * contributes nothing and why.
 */
export const InheritedSection = ({ here, prov, loadError, onRetry, catalog, onOverride }: Props) => {
  const inheriting = prov?.inheritable === true;
  const broken = prov?.inherited === null;
  const count = inheriting && prov.inherited ? Object.keys(prov.inherited).length : null;

  return (
    <section aria-labelledby="inherited-heading" className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 id="inherited-heading" className="flex flex-wrap items-center gap-x-2 text-lg font-semibold">
          <span>
            Inherited {count !== null && <span className="font-normal text-gray-600 dark:text-gray-400">{count}</span>}
          </span>
          {prov?.stack.some(isCard) && (
            <Badge
              color="gray"
              size="xs"
              className="inline-flex align-middle font-medium hover:bg-gray-100 dark:hover:bg-gray-700"
            >
              <span className="inline-flex items-center gap-1">
                <HiLockClosed aria-hidden="true" className="h-3 w-3" />
                Read only
              </span>
            </Badge>
          )}
        </h2>
        {count !== null && (
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {count > 0
              ? "Served here because this chamber doesn't define them, or a parent seals them. Parent chambers are checked nearest first, and the first that defines a flag wins, unless a farther one seals it."
              : 'Nothing is inherited here.'}
          </p>
        )}
        {prov && !inheriting && (
          <p className="text-sm text-gray-600 dark:text-gray-400">
            This server doesn&rsquo;t apply inheritance, so this chamber serves only its own flags. Flags in parent
            chambers are not served here.
          </p>
        )}
      </div>

      {loadError && (
        <Alert color="warning">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <span>Couldn&rsquo;t load the parent chambers: {loadError}</span>
            <RetryButton onRetry={onRetry} />
          </div>
        </Alert>
      )}

      {inheriting && (
        <ol aria-label="Parent chambers, nearest first" className="flex flex-col gap-3">
          {prov.stack.map((entry) =>
            isCard(entry) ? (
              <LayerCard key={entry.path} entry={entry} here={here} catalog={catalog} onOverride={onOverride} />
            ) : (
              <StackLine key={entry.path} entry={entry} here={here} broken={broken} />
            )
          )}
        </ol>
      )}
    </section>
  );
};
