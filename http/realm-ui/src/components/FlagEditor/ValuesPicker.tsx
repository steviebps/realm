import { useId, useState } from 'react';
import { Badge, TextInput } from 'flowbite-react';
import { HiX } from 'react-icons/hi';
import { Catalog } from '../../models/response';
import { labelFor } from '../../utils/describe';

type Props = {
  attribute: string;
  values: string[];
  catalog: Catalog;
  onChange: (values: string[]) => void;
};

/**
 * A multi-value input that searches the attribute catalog by label, so editors
 * can pick "Downtown" instead of pasting an opaque id. Unknown values can still
 * be typed and added with Enter.
 */
export const ValuesPicker = ({ attribute, values, catalog, onChange }: Props) => {
  const [text, setText] = useState('');
  const listId = useId();
  const known = catalog[attribute] ?? {};
  const query = text.trim().toLowerCase();
  const suggestions = Object.entries(known)
    .filter(
      ([v, label]) => !values.includes(v) && (label.toLowerCase().includes(query) || v.toLowerCase().includes(query))
    )
    .slice(0, 50);

  const add = (raw: string) => {
    const input = raw.trim();
    if (!input) return;
    // Accept either an id or an exact label from the catalog.
    const byLabel = Object.entries(known).find(([, label]) => label.toLowerCase() === input.toLowerCase());
    const value = known[input] !== undefined ? input : (byLabel?.[0] ?? input);
    if (!values.includes(value)) onChange([...values, value]);
    setText('');
  };

  return (
    <div className="flex flex-col gap-2">
      {values.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {values.map((v) => (
            <Badge key={v} color={known[v] ? 'info' : 'gray'} title={v} className="flex items-center">
              {labelFor(catalog, attribute, v)}
              <button
                type="button"
                aria-label={`Remove ${labelFor(catalog, attribute, v)}`}
                className="ml-1 align-middle"
                onClick={() => onChange(values.filter((x) => x !== v))}
              >
                <HiX />
              </button>
            </Badge>
          ))}
        </div>
      )}
      <TextInput
        sizing="sm"
        list={listId}
        placeholder={Object.keys(known).length ? `Search ${attribute}…` : 'Type a value and press Enter'}
        value={text}
        onChange={(e) => {
          const next = e.target.value;
          // Choosing a datalist option inserts its value (the id) directly.
          if (known[next] !== undefined && !values.includes(next)) {
            onChange([...values, next]);
            setText('');
            return;
          }
          setText(next);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault();
            add(text);
          }
        }}
        onBlur={() => add(text)}
      />
      <datalist id={listId}>
        {suggestions.map(([v, label]) => (
          <option key={v} value={v}>
            {label}
          </option>
        ))}
      </datalist>
    </div>
  );
};
