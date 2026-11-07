import { useId, useState } from 'react';
import { Badge } from 'flowbite-react';
import { HiChevronRight } from 'react-icons/hi';
import { Link } from 'react-router-dom';
import { Catalog, Flag, FlagValue, RuleType } from '../models/response';
import { describeDefinition, formatValue, same } from '../utils/describe';
import { Served } from '../utils/provenance';
import { scheduleEnded } from '../utils/schedule';
import { encodePath } from '../utils/path';
import { plural, trimSuffix } from '../utils/strings';

/** Rows share the table's column tracks so every column lines up. */
const ROW = 'sm:col-span-4 sm:grid sm:grid-cols-subgrid sm:items-center';

/** The focus ring links use across the UI. */
const FOCUS_RING = 'rounded focus-visible:outline-2 focus-visible:outline-primary-500';

/** A visual separator that screen readers read as a pause instead of a symbol. */
export const Sep = ({ symbol = '·' }: { symbol?: string }) => (
  <>
    <span aria-hidden="true"> {symbol} </span>
    <span className="sr-only">, </span>
  </>
);

/** A chamber path or flag key in monospace, wrapping anywhere so long ones never overflow. */
export const Mono = ({ children }: { children: React.ReactNode }) => (
  <span className="font-mono [overflow-wrap:anywhere]">{children}</span>
);

/** A chamber named in a sentence: "this chamber" when it is the one being viewed, else its path. */
export const ChamberRef = ({ path, here }: { path: string; here: string }) =>
  path === here ? 'this chamber' : <Mono>{path}</Mono>;

/** A link to the chamber at path, shown as the path in monospace. */
export const PathLink = ({ path, children }: { path: string; children?: React.ReactNode }) => (
  <Link
    to={encodePath(trimSuffix(path, '/')) || '/'}
    className={`text-primary-700 hover:underline dark:text-primary-300 ${FOCUS_RING}`}
  >
    {children}
    <Mono>{path}</Mono>
  </Link>
);

/** A list of flags with a column header; rows are FlagRows. */
export const FlagTable = ({
  label,
  labelledBy,
  headerHidden,
  children,
}: {
  label?: string;
  labelledBy?: string;
  /** Keeps the column header for screen readers only. */
  headerHidden?: boolean;
  children: React.ReactNode;
}) => (
  <div
    role="table"
    aria-label={label}
    aria-labelledby={labelledBy}
    className="sm:grid sm:grid-cols-[minmax(0,5fr)_minmax(0,4fr)_minmax(0,3fr)_auto] sm:gap-x-4"
  >
    <div
      role="row"
      className={
        headerHidden
          ? 'sr-only'
          : `hidden border-b border-gray-200 px-4 py-2 text-xs font-medium text-gray-500 dark:border-gray-700 dark:text-gray-400 ${ROW}`
      }
    >
      <span role="columnheader">Flag</span>
      <span role="columnheader">Default</span>
      <span role="columnheader">Targeting</span>
      <span role="columnheader" className="sr-only">
        Actions
      </span>
    </div>
    <div role="rowgroup" className={ROW}>
      {children}
    </div>
  </div>
);

/** A served value: booleans as an on/off indicator, everything else as code. */
export const FlagValueView = ({ type, value, small }: { type: RuleType; value: FlagValue; small?: boolean }) => {
  if (type === 'boolean') {
    const on = value === true;
    const size = small ? 'h-2 w-2' : 'h-2.5 w-2.5';
    return (
      <span className={`inline-flex items-center gap-1.5 font-mono ${small ? 'text-xs' : 'text-sm'}`}>
        <span
          aria-hidden="true"
          className={
            on
              ? `${size} rounded-full bg-primary-600 ring-2 ring-primary-100 dark:bg-primary-400 dark:ring-primary-900`
              : `${size} rounded-full border-2 border-gray-400 dark:border-gray-500`
          }
        />
        {on ? 'true' : 'false'}
      </span>
    );
  }
  const text = formatValue(value);
  return (
    <code
      className={`inline-block max-w-full truncate rounded-md bg-gray-100 px-1.5 py-0.5 align-middle text-gray-800 dark:bg-gray-700 dark:text-gray-200 ${small ? 'text-xs' : 'text-sm'}`}
      title={text}
    >
      {text}
    </code>
  );
};

/** What a flag that overrides another definition is served instead of. */
const InsteadOf = ({ served }: { served: Served }) => {
  const [{ path, flag: was }] = served.overrides;
  const now = served.flag;
  if (same(now, was)) {
    return (
      <span title={`Changes to it in ${path} no longer reach this chamber.`}>
        same as <Mono>{path}</Mono>&rsquo;s definition
      </span>
    );
  }
  const rules = was.targeting?.length ?? 0;
  const looksSame = was.type === now.type && same(was.value, now.value) && rules === (now.targeting?.length ?? 0);
  return (
    <span>
      instead of <FlagValueView small type={was.type} value={was.value} />
      {rules > 0 && ` · ${plural(rules, 'rule')}`}
      {was.type !== now.type && ` (${was.type})`}
      {looksSame && ' (rules differ)'}
    </span>
  );
};

/** The chambers that define a flag, nearest first, and what each one's definition is. */
const DefinitionChain = ({ served, here, catalog }: { served: Served; here: string; catalog: Catalog }) => {
  const servedHere = served.source === here;
  return (
    <div className="flex flex-col gap-1.5">
      <p>Defined in these chambers, nearest first:</p>
      <ol className="flex flex-col gap-2">
        <li className="flex flex-col gap-x-3 sm:flex-row">
          <span className="shrink-0 sm:w-40">{servedHere ? 'This chamber' : <PathLink path={served.source} />}</span>
          <span>This definition is served here</span>
        </li>
        {served.overrides.map(({ path, flag }) => (
          <li key={path} className="flex flex-col gap-x-3 sm:flex-row">
            <span className="shrink-0 sm:w-40">
              <PathLink path={path} />
            </span>
            <div className="flex flex-col">
              <span>
                Overridden by <ChamberRef path={served.source} here={here} />
              </span>
              {describeDefinition(flag, catalog).map((line) => (
                <span key={line} className="text-gray-600 dark:text-gray-400">
                  {line}
                </span>
              ))}
              {flag.type !== served.flag.type && <span>Type differs: {flag.type}.</span>}
            </div>
          </li>
        ))}
      </ol>
    </div>
  );
};

/**
 * One flag: its key and type, the value it serves by default, and its
 * targeting at a glance. The key opens the flag's details: its definition in
 * plain language and, when it overrides other chambers' definitions, each of
 * them. A row followed by its details panel forms two table rows.
 */
export const FlagRow = ({
  name,
  flag,
  served,
  here,
  catalog,
  muted,
  actions,
  detailActions,
}: {
  name: string;
  flag: Flag;
  /** Where the flag is served from, when the server reported it. */
  served?: Served;
  /** The path of the chamber being viewed. */
  here: string;
  catalog: Catalog;
  muted?: boolean;
  /** Controls at the end of the row. */
  actions?: React.ReactNode;
  /** Controls at the end of the details panel. */
  detailActions?: React.ReactNode;
}) => {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const rules = flag.targeting ?? [];
  const rollouts = rules.filter((r) => r.rollout).length;
  const scheduled = rules.filter((r) => r.schedule && !scheduleEnded(r.schedule)).length;
  const overridden = served?.overrides[0]?.path;
  return (
    <>
      <div
        role="row"
        className={`flex flex-wrap items-center gap-x-4 gap-y-2 border-t border-gray-200 px-4 py-3 first:border-t-0 dark:border-gray-700 ${ROW} ${muted ? 'text-gray-600 dark:text-gray-400' : ''}`}
      >
        <div role="cell" className="min-w-0 basis-full sm:basis-auto">
          <button
            type="button"
            aria-expanded={open}
            aria-controls={panelId}
            onClick={() => setOpen(!open)}
            className={`-ml-1 flex min-h-8 max-w-full items-center gap-1 px-1 text-left font-mono text-sm font-semibold hover:text-primary-700 sm:min-h-6 dark:hover:text-primary-300 ${FOCUS_RING}`}
          >
            <HiChevronRight
              aria-hidden="true"
              className={`h-4 w-4 shrink-0 text-gray-500 motion-safe:transition-transform dark:text-gray-400 ${open ? 'rotate-90' : ''}`}
            />
            <span className="truncate" title={name}>
              {name}
            </span>
            <span className="sr-only"> details</span>
          </button>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-5 text-xs text-gray-500 dark:text-gray-400">
            {flag.type}
            {overridden && (
              <Badge
                color="info"
                size="xs"
                className="inline-flex align-middle font-medium hover:bg-primary-100 dark:hover:bg-primary-900"
                title={`Replaces ${overridden}'s definition entirely: its default and all of its rules.`}
              >
                Overrides <Mono>{overridden}</Mono>
              </Badge>
            )}
          </div>
        </div>
        <div role="cell" className="flex min-w-0 flex-col items-start gap-1">
          <FlagValueView type={flag.type} value={flag.value} />
          {served && served.overrides.length > 0 && (
            <span className="text-xs text-gray-600 dark:text-gray-400">
              <InsteadOf served={served} />
            </span>
          )}
        </div>
        <div
          role="cell"
          className={`text-sm text-gray-600 dark:text-gray-400 ${rules.length ? '' : 'hidden sm:block'}`}
        >
          {rules.length === 0 ? (
            <span className="text-gray-500 dark:text-gray-400">None</span>
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
          {actions}
        </div>
      </div>
      {/* The row stays for aria-controls; its contents are built only when opened. */}
      <div role="row" id={panelId} hidden={!open} className={ROW}>
        {open && (
          <div role="cell" aria-colspan={4} className="px-4 pb-4 sm:col-span-4">
            <div className="ml-1.5 flex flex-col gap-3 border-l-2 border-gray-300 pl-4 text-sm text-gray-700 dark:border-gray-600 dark:text-gray-300">
              <div className="flex flex-col">
                {describeDefinition(flag, catalog).map((line) => (
                  <span key={line}>{line}</span>
                ))}
              </div>
              {served && served.overrides.length > 0 && (
                <DefinitionChain served={served} here={here} catalog={catalog} />
              )}
              {served && served.source === here && overridden && (
                <p>
                  If you delete it here, this chamber serves <Mono>{overridden}</Mono>&rsquo;s definition again.
                </p>
              )}
              {detailActions}
            </div>
          </div>
        )}
      </div>
    </>
  );
};
