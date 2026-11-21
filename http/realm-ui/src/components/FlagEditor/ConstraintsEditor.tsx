import { useId, useState } from 'react';
import { HelperText, Label, Textarea, TextInput } from 'flowbite-react';
import { RuleType, Schema } from '../../models/response';

type Props = {
  type: RuleType;
  constraints: Schema | undefined;
  /** Called with the new constraints, or undefined when none are left. */
  onChange: (constraints: Schema | undefined) => void;
  /** Reports whether the input can be read (a custom schema's JSON may not parse). */
  onValidityChange: (valid: boolean) => void;
};

/** The keywords the fields below edit, by flag type; others are kept as they are and listed. */
const EDITED: Partial<Record<RuleType, (keyof Schema)[]>> = {
  number: ['minimum', 'maximum', 'multipleOf'],
  string: ['enum', 'maxLength'],
};

/** Returns constraints with key set, or removed when undefined; undefined when no key is left. */
const withKey = (constraints: Schema | undefined, key: keyof Schema, value: unknown): Schema | undefined => {
  const next: Record<string, unknown> = { ...constraints };
  if (value === undefined) delete next[key];
  else next[key] = value;
  return Object.keys(next).length ? next : undefined;
};

const numberOrUndefined = (s: string) => (s.trim() === '' ? undefined : Number(s));

/**
 * Edits the values a flag may take. Number and string flags get fields for the common constraints; a custom flag's
 * are a JSON schema. The server checks them, and the editor explains its errors before saving.
 */
export const ConstraintsEditor = ({ type, constraints, onChange, onValidityChange }: Props) => {
  const id = useId();
  // free text kept apart from the constraints, so typing a blank line or half-written JSON isn't undone
  const [values, setValues] = useState(() => (constraints?.enum ?? []).map(String).join('\n'));
  const [json, setJSON] = useState(() => (constraints ? JSON.stringify(constraints, null, 2) : ''));
  const [jsonError, setJSONError] = useState('');

  const edited = EDITED[type];
  const others = edited
    ? Object.fromEntries(Object.entries(constraints ?? {}).filter(([k]) => !edited.includes(k as keyof Schema)))
    : {};
  const set = (key: keyof Schema, value: unknown) => onChange(withKey(constraints, key, value));

  if (!edited) {
    // custom flags, and the rare boolean flag with constraints
    if (type === 'boolean' && !constraints) return null;
    return (
      <div>
        <Label htmlFor={`${id}-schema`}>Schema</Label>
        <Textarea
          id={`${id}-schema`}
          rows={5}
          className="font-mono text-sm"
          placeholder={
            '{\n  "type": "object",\n  "required": ["text"],\n  "properties": { "text": { "maxLength": 140 } }\n}'
          }
          value={json}
          color={jsonError ? 'failure' : undefined}
          aria-describedby={`${id}-schema-help`}
          onChange={(e) => {
            setJSON(e.target.value);
            if (e.target.value.trim() === '') {
              setJSONError('');
              onValidityChange(true);
              onChange(undefined);
              return;
            }
            try {
              const parsed = JSON.parse(e.target.value) as Schema;
              setJSONError('');
              onValidityChange(true);
              onChange(parsed);
            } catch (err) {
              setJSONError((err as Error).message);
              onValidityChange(false);
            }
          }}
        />
        {jsonError ? (
          <HelperText color="failure">Invalid JSON: {jsonError}</HelperText>
        ) : (
          <HelperText id={`${id}-schema-help`}>
            A JSON Schema with only these keywords: type, enum, minimum, maximum, multipleOf, minLength, maxLength,
            properties, required, additionalProperties (true or false), and items. Leave it empty to allow any value.
          </HelperText>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {type === 'number' && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div>
            <Label htmlFor={`${id}-min`}>Minimum</Label>
            <TextInput
              id={`${id}-min`}
              type="number"
              step="any"
              value={constraints?.minimum ?? ''}
              onChange={(e) => set('minimum', numberOrUndefined(e.target.value))}
            />
          </div>
          <div>
            <Label htmlFor={`${id}-max`}>Maximum</Label>
            <TextInput
              id={`${id}-max`}
              type="number"
              step="any"
              value={constraints?.maximum ?? ''}
              onChange={(e) => set('maximum', numberOrUndefined(e.target.value))}
            />
          </div>
          <div>
            <Label htmlFor={`${id}-step`}>Multiple of</Label>
            <TextInput
              id={`${id}-step`}
              type="number"
              step="any"
              min={0}
              placeholder="1 for whole numbers"
              value={constraints?.multipleOf ?? ''}
              onChange={(e) => set('multipleOf', numberOrUndefined(e.target.value))}
            />
          </div>
        </div>
      )}
      {type === 'string' && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-[2fr_1fr]">
          <div>
            <Label htmlFor={`${id}-values`}>Only these values</Label>
            <Textarea
              id={`${id}-values`}
              rows={3}
              className="font-mono text-sm"
              placeholder={'standard\nexpress'}
              aria-describedby={`${id}-values-help`}
              value={values}
              onChange={(e) => {
                setValues(e.target.value);
                const list = e.target.value.split('\n').filter((v) => v !== '');
                set('enum', list.length ? list : undefined);
              }}
            />
            <HelperText id={`${id}-values-help`}>One per line; leave empty to allow any text.</HelperText>
          </div>
          <div>
            <Label htmlFor={`${id}-maxlength`}>Maximum length</Label>
            <TextInput
              id={`${id}-maxlength`}
              type="number"
              min={0}
              step={1}
              value={constraints?.maxLength ?? ''}
              onChange={(e) => set('maxLength', numberOrUndefined(e.target.value))}
            />
          </div>
        </div>
      )}
      {Object.keys(others).length > 0 && (
        <p className="text-sm text-gray-600 dark:text-gray-400">
          Also: <code className="font-mono text-xs">{JSON.stringify(others)}</code>
        </p>
      )}
    </div>
  );
};
