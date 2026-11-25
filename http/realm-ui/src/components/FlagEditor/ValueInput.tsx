import { useState } from 'react';
import { HelperText, Select, Textarea, TextInput, ToggleSwitch } from 'flowbite-react';
import { FlagValue, FlagType } from '../../models/response';
import { allowedValues, Bound, numberRange, refusal } from '../../utils/constraints';

type Props = {
  id: string;
  type: FlagType;
  value: FlagValue;
  onChange: (value: FlagValue) => void;
  /** Reports whether the current input is valid (custom JSON may not parse). */
  onValidityChange?: (valid: boolean) => void;
  /** The constraints the value must satisfy: they bound the input, and a value one refuses is explained. */
  bounds?: Bound[];
};

export const ValueInput = ({ id, type, value, onChange, onValidityChange, bounds = [] }: Props) => {
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
  const refused = jsonError ? undefined : refusal(bounds, value);
  const errorId = `${id}-refused`;
  // what marks an input whose value is refused
  const invalid = refused
    ? { color: 'failure' as const, 'aria-invalid': true, 'aria-describedby': errorId }
    : { color: jsonError ? ('failure' as const) : undefined };

  const input = () => {
    switch (type) {
      case 'boolean':
        return <ToggleSwitch id={id} checked={value === true} label={value ? 'true' : 'false'} onChange={onChange} />;
      case 'number': {
        const { min, max, step } = numberRange(schemas);
        return (
          <TextInput
            id={id}
            type="number"
            min={min}
            max={max}
            step={step ?? 'any'}
            value={value as number}
            {...invalid}
            onChange={(e) => onChange(e.target.value === '' ? 0 : Number(e.target.value))}
          />
        );
      }
      case 'string': {
        const allowed = allowedValues(schemas);
        if (!allowed) {
          return (
            <TextInput
              id={id}
              type="text"
              value={value as string}
              {...invalid}
              onChange={(e) => onChange(e.target.value)}
            />
          );
        }
        // a value saved before the constraints stays selectable, so the editor never changes it unasked
        const options = allowed.includes(value) ? allowed : [value, ...allowed];
        return (
          <Select id={id} value={value as string} {...invalid} onChange={(e) => onChange(e.target.value)}>
            {options.map((v) => (
              <option key={String(v)} value={String(v)}>
                {String(v)}
              </option>
            ))}
          </Select>
        );
      }
      case 'custom':
        return (
          <>
            <Textarea
              id={id}
              rows={4}
              className="font-mono text-sm"
              value={json}
              {...invalid}
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
          </>
        );
    }
  };

  return (
    <div>
      {input()}
      {refused && (
        <HelperText id={errorId} color="failure">
          Not allowed: {refused}.
        </HelperText>
      )}
    </div>
  );
};
