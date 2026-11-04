import { useId } from 'react';
import { Button, Card, Label, RangeSlider, Select, TextInput, ToggleSwitch } from 'flowbite-react';
import { HiArrowDown, HiArrowUp, HiPlus, HiTrash } from 'react-icons/hi';
import { Catalog, Clause, Operator, RuleType, TargetingRule } from '../../models/response';
import { OPERATOR_LABELS, describeRule } from '../../utils/describe';
import { fromLocalInput, nextHour, toLocalInput } from '../../utils/schedule';
import { ValueInput } from './ValueInput';
import { ValuesPicker } from './ValuesPicker';

type Props = {
  index: number;
  count: number;
  rule: TargetingRule;
  type: RuleType;
  catalog: Catalog;
  attributes: string[];
  onChange: (rule: TargetingRule) => void;
  onMove: (delta: -1 | 1) => void;
  onRemove: () => void;
  onValidityChange: (valid: boolean) => void;
};

const isSemver = (op: Operator) => op === 'semverGte' || op === 'semverLte';

const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

export const RuleEditor = ({
  index,
  count,
  rule,
  type,
  catalog,
  attributes,
  onChange,
  onMove,
  onRemove,
  onValidityChange,
}: Props) => {
  const attrListId = useId();
  const clauses = rule.clauses ?? [];

  const setClause = (i: number, clause: Clause) =>
    onChange({ ...rule, clauses: clauses.map((c, j) => (j === i ? clause : c)) });

  return (
    <Card className="[&>div]:gap-3 [&>div]:p-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h4 className="font-semibold">Rule {index + 1}</h4>
          <p className="text-sm text-gray-500 dark:text-gray-400">{describeRule(rule, catalog)}</p>
        </div>
        <div className="flex gap-1">
          <Button size="xs" color="light" disabled={index === 0} onClick={() => onMove(-1)} aria-label="Move rule up">
            <HiArrowUp />
          </Button>
          <Button
            size="xs"
            color="light"
            disabled={index === count - 1}
            onClick={() => onMove(1)}
            aria-label="Move rule down"
          >
            <HiArrowDown />
          </Button>
          <Button
            size="xs"
            color="light"
            className="text-gray-500 hover:text-red-700 dark:text-gray-400 dark:hover:text-red-400"
            onClick={onRemove}
            aria-label="Delete rule"
          >
            <HiTrash />
          </Button>
        </div>
      </div>

      <div>
        <Label htmlFor={`${attrListId}-desc`}>Description</Label>
        <TextInput
          id={`${attrListId}-desc`}
          sizing="sm"
          placeholder="e.g. Pilot locations"
          value={rule.description ?? ''}
          onChange={(e) => onChange({ ...rule, description: e.target.value || undefined })}
        />
      </div>

      <div className="flex flex-col gap-2">
        <Label>
          Conditions{' '}
          {clauses.length === 0 && <span className="font-normal text-gray-500">(none: matches everyone)</span>}
        </Label>
        {clauses.map((clause, i) => (
          <div
            key={i}
            className="grid grid-cols-1 gap-2 rounded-lg border border-gray-200 p-2 sm:grid-cols-[1fr_1fr_2fr_auto] dark:border-gray-700"
          >
            <TextInput
              sizing="sm"
              className="[&_input]:font-mono"
              list={attrListId}
              placeholder="attribute"
              aria-label="Attribute"
              value={clause.attribute}
              onChange={(e) => setClause(i, { ...clause, attribute: e.target.value })}
            />
            <Select
              sizing="sm"
              aria-label="Operator"
              value={clause.op}
              onChange={(e) => {
                const op = e.target.value as Operator;
                setClause(i, { ...clause, op, values: isSemver(op) ? clause.values.slice(0, 1) : clause.values });
              }}
            >
              {(Object.keys(OPERATOR_LABELS) as Operator[]).map((op) => (
                <option key={op} value={op}>
                  {OPERATOR_LABELS[op]}
                </option>
              ))}
            </Select>
            {isSemver(clause.op) ? (
              <TextInput
                sizing="sm"
                className="[&_input]:font-mono"
                placeholder="v1.2.0"
                aria-label="Version"
                value={clause.values[0] ?? ''}
                onChange={(e) => setClause(i, { ...clause, values: e.target.value ? [e.target.value] : [] })}
              />
            ) : (
              <ValuesPicker
                attribute={clause.attribute}
                values={clause.values}
                catalog={catalog}
                onChange={(values) => setClause(i, { ...clause, values })}
              />
            )}
            <Button
              size="xs"
              color="light"
              aria-label="Remove condition"
              onClick={() => onChange({ ...rule, clauses: clauses.filter((_, j) => j !== i) })}
            >
              <HiTrash />
            </Button>
          </div>
        ))}
        <div>
          <Button
            size="xs"
            color="light"
            onClick={() => onChange({ ...rule, clauses: [...clauses, { attribute: '', op: 'in', values: [] }] })}
          >
            <HiPlus className="mr-1" /> Add condition
          </Button>
        </div>
        <datalist id={attrListId}>
          {attributes.map((a) => (
            <option key={a} value={a} />
          ))}
        </datalist>
      </div>

      <div>
        <Label>Serve</Label>
        <ValueInput
          id={`${attrListId}-value`}
          type={type}
          value={rule.value}
          onChange={(value) => onChange({ ...rule, value })}
          onValidityChange={onValidityChange}
        />
      </div>

      <div className="flex flex-col gap-2">
        <ToggleSwitch
          checked={!!rule.rollout}
          label="Gradual rollout"
          onChange={(on) => onChange({ ...rule, rollout: on ? { percentage: 10 } : undefined })}
        />
        {rule.rollout && (
          <div className="grid grid-cols-1 items-end gap-3 sm:grid-cols-[2fr_1fr]">
            <div>
              <Label htmlFor={`${attrListId}-pct`}>
                {rule.rollout.percentage}% of matching {rule.rollout.bucketBy || 'key'}s
              </Label>
              <RangeSlider
                id={`${attrListId}-pct`}
                min={0}
                max={100}
                step={1}
                value={rule.rollout.percentage}
                onChange={(e) =>
                  onChange({ ...rule, rollout: { ...rule.rollout!, percentage: Number(e.target.value) } })
                }
              />
            </div>
            <div>
              <Label htmlFor={`${attrListId}-by`}>Bucket by</Label>
              <TextInput
                id={`${attrListId}-by`}
                sizing="sm"
                className="[&_input]:font-mono"
                list={attrListId}
                placeholder="key"
                value={rule.rollout.bucketBy ?? ''}
                onChange={(e) =>
                  onChange({ ...rule, rollout: { ...rule.rollout!, bucketBy: e.target.value || undefined } })
                }
              />
            </div>
          </div>
        )}
      </div>

      <div className="flex flex-col gap-2">
        <ToggleSwitch
          checked={!!rule.schedule}
          label="Only during a window of time"
          onChange={(on) => onChange({ ...rule, schedule: on ? nextHour() : undefined })}
        />
        {rule.schedule && (
          <>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div>
                <Label htmlFor={`${attrListId}-start`}>Starts</Label>
                <TextInput
                  id={`${attrListId}-start`}
                  type="datetime-local"
                  sizing="sm"
                  value={toLocalInput(rule.schedule.start)}
                  onChange={(e) =>
                    onChange({ ...rule, schedule: { ...rule.schedule, start: fromLocalInput(e.target.value) } })
                  }
                />
              </div>
              <div>
                <Label htmlFor={`${attrListId}-end`}>Ends</Label>
                <TextInput
                  id={`${attrListId}-end`}
                  type="datetime-local"
                  sizing="sm"
                  value={toLocalInput(rule.schedule.end)}
                  onChange={(e) =>
                    onChange({ ...rule, schedule: { ...rule.schedule, end: fromLocalInput(e.target.value) } })
                  }
                />
              </div>
            </div>
            <p className="text-xs text-gray-500 dark:text-gray-400">
              Times are in your time zone ({timeZone}). Leave one empty for an open-ended window.
            </p>
          </>
        )}
      </div>
    </Card>
  );
};
