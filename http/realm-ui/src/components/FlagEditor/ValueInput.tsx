import { useEffect, useState } from 'react';
import { HelperText, Textarea, TextInput, ToggleSwitch } from 'flowbite-react';
import { FlagValue, RuleType } from '../../models/response';

type Props = {
  id: string;
  type: RuleType;
  value: FlagValue;
  onChange: (value: FlagValue) => void;
  /** Reports whether the current input is valid (custom JSON may not parse). */
  onValidityChange?: (valid: boolean) => void;
};

export const ValueInput = ({ id, type, value, onChange, onValidityChange }: Props) => {
  const [json, setJSON] = useState(() => JSON.stringify(value, null, 2));
  const [jsonError, setJSONError] = useState('');

  useEffect(() => {
    if (type === 'custom') {
      setJSON(JSON.stringify(value, null, 2));
    }
    // only resync when switching to a different flag/rule, not on every keystroke
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, type]);

  switch (type) {
    case 'boolean':
      return <ToggleSwitch id={id} checked={value === true} label={value ? 'true' : 'false'} onChange={onChange} />;
    case 'number':
      return (
        <TextInput
          id={id}
          type="number"
          step="any"
          value={String(value as number)}
          onChange={(e) => onChange(e.target.value === '' ? 0 : Number(e.target.value))}
        />
      );
    case 'string':
      return <TextInput id={id} type="text" value={value as string} onChange={(e) => onChange(e.target.value)} />;
    case 'custom':
      return (
        <div>
          <Textarea
            id={id}
            rows={4}
            className="font-mono text-sm"
            value={json}
            color={jsonError ? 'failure' : undefined}
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
        </div>
      );
  }
};
