import { useState } from 'react';
import { HelperText, Select, Textarea, TextInput, ToggleSwitch } from 'flowbite-react';
import { FlagValue, RuleType } from '../../models/response';
import { allowedValues, Bound, formatValueError, numberRange, validate } from '../../utils/constraints';

type Props = {
  id: string;
  type: RuleType;
  value: FlagValue;
  onChange: (value: FlagValue) => void;
  /** Reports whether the current input is valid (custom JSON may not parse). */
  onValidityChange?: (valid: boolean) => void;
  /** The constraints the value must satisfy: they bound the input, and a value one refuses is explained. */
  bounds?: Bound[];
  /** The path of the chamber being edited, so the message names only other chambers' constraints. */
  here?: string;
};

/** Why the first of bounds refuses value, naming the chamber whose constraints it is when not here. */
const refusal = (bounds: Bound[], value: unknown, here?: string) => {
  for (const { path, schema } of bounds) {
    const err = validate(schema, value);
    if (err) return `Not allowed: ${formatValueError(err)}${path === here ? '' : ` (constraints of ${path})`}.`;
  }
  return undefined;
};

export const ValueInput = ({ id, type, value, onChange, onValidityChange, bounds = [], here }: Props) => {
  const [json, setJSON] = useState(() => JSON.stringify(value, null, 2));
  const [jsonError, setJSONError] = useState('');

  // Only resync when switching to a different flag/rule, not on every keystroke.
  const [synced, setSynced] = useState({ id, type });
  if (synced.id !== id || synced.type !== type) {
    setSynced({ id, type });
    if (type === 'custom') {
      setJSON(JSON.stringify(value, null, 2));
    }
  }

  const schemas = bounds.map((b) => b.schema);
  // a custom value that doesn't parse is explained by its JSON error instead
  const refused = jsonError ? undefined : refusal(bounds, value, here);
  const errorId = `${id}-refused`;
  const explained = refused && (
    <HelperText id={errorId} color="failure">
      {refused}
    </HelperText>
  );

  switch (type) {
    case 'boolean':
      return (
        <div>
          <ToggleSwitch id={id} checked={value === true} label={value ? 'true' : 'false'} onChange={onChange} />
          {explained}
        </div>
      );
    case 'number': {
      const { min, max, step } = numberRange(schemas);
      return (
        <div>
          <TextInput
            id={id}
            type="number"
            min={min}
            max={max}
            step={step ?? 'any'}
            value={value as number}
            color={refused ? 'failure' : undefined}
            aria-invalid={refused ? true : undefined}
            aria-describedby={refused ? errorId : undefined}
            onChange={(e) => onChange(e.target.value === '' ? 0 : Number(e.target.value))}
          />
          {explained}
        </div>
      );
    }
    case 'string': {
      const allowed = allowedValues(schemas);
      if (allowed) {
        // a value saved before the constraints stays selectable, so the editor never changes it unasked
        const options = allowed.includes(value) ? allowed : [value, ...allowed];
        return (
          <div>
            <Select
              id={id}
              value={value as string}
              color={refused ? 'failure' : undefined}
              aria-invalid={refused ? true : undefined}
              aria-describedby={refused ? errorId : undefined}
              onChange={(e) => onChange(e.target.value)}
            >
              {options.map((v) => (
                <option key={String(v)} value={String(v)}>
                  {String(v)}
                </option>
              ))}
            </Select>
            {explained}
          </div>
        );
      }
      return (
        <div>
          <TextInput
            id={id}
            type="text"
            value={value as string}
            color={refused ? 'failure' : undefined}
            aria-invalid={refused ? true : undefined}
            aria-describedby={refused ? errorId : undefined}
            onChange={(e) => onChange(e.target.value)}
          />
          {explained}
        </div>
      );
    }
    case 'custom':
      return (
        <div>
          <Textarea
            id={id}
            rows={4}
            className="font-mono text-sm"
            value={json}
            color={jsonError || refused ? 'failure' : undefined}
            aria-invalid={jsonError || refused ? true : undefined}
            aria-describedby={refused ? errorId : undefined}
            onChange={(e) => {
              setJSON(e.target.value);
              try {
                const parsed = JSON.parse(e.target.value) as FlagValue;
                setJSONError('');
                onValidityChange?.(true);
                onChange(parsed);
              } catch (err) {
                setJSONError((err as Error).message);
                onValidityChange?.(false);
              }
            }}
          />
          {jsonError && <HelperText color="failure">Invalid JSON: {jsonError}</HelperText>}
          {explained}
        </div>
      );
  }
};
