import { useState } from 'react';
import { Alert, Button, Checkbox, Label, Modal, ModalBody, ModalFooter, ModalHeader } from 'flowbite-react';
import { HiPause, HiPlus } from 'react-icons/hi';
import { Catalog, Flag, TargetingRule } from '../../models/response';
import { rulesOf, targetingPaused, validateFlag, withRules, withSealed } from '../../utils/flags';
import { RuleEditor } from './RuleEditor';
import { ValueInput } from './ValueInput';

type Props = {
  name: string;
  flag: Flag;
  catalog: Catalog;
  attributes: string[];
  onCancel: () => void;
  /** Called with the edited flag; the caller confirms the diff and saves. */
  onReview: (flag: Flag) => void;
};

// Rules have no stable identity of their own, so the editor gives each one a
// client-side key (never sent to the server) for React keys and validity.
let nextRuleKey = 0;
const newRuleKey = () => `rule-${nextRuleKey++}`;

export const FlagEditor = ({ name, flag, catalog, attributes, onCancel, onReview }: Props) => {
  const [draft, setDraft] = useState<Flag>(() => structuredClone(flag));
  // Kept apart from the draft, which can't be paused without rules, so that rules added while targeting is off stay
  // paused.
  const [paused, setPaused] = useState(() => targetingPaused(flag));
  const [ruleKeys, setRuleKeys] = useState<string[]>(() => rulesOf(flag).map(newRuleKey));
  const [invalid, setInvalid] = useState<Set<string>>(new Set());
  const [showProblems, setShowProblems] = useState(false);
  const rules = rulesOf(draft);
  const problems = validateFlag(draft);

  // setRules replaces the rules, and their keys, which must stay in step.
  const setRules = (next: TargetingRule[], keys: string[]) => {
    setDraft(withRules(draft, next, paused));
    setRuleKeys(keys);
  };
  const setValidity = (key: string) => (valid: boolean) =>
    setInvalid((prev) => {
      const next = new Set(prev);
      if (valid) next.delete(key);
      else next.add(key);
      return next;
    });

  return (
    <Modal show size="4xl" onClose={onCancel}>
      <ModalHeader>
        <span className="font-mono">{name}</span>{' '}
        <span className="ml-2 text-sm font-normal text-gray-500 dark:text-gray-400">{draft.type}</span>
      </ModalHeader>
      <ModalBody>
        <div className="flex flex-col gap-4">
          <p className="text-sm text-gray-600 dark:text-gray-400">
            Rules are checked top to bottom; the first rule that applies decides the value. If none applies, the default
            is served.
          </p>

          {paused && (
            <Alert color="warning" icon={HiPause}>
              Targeting is off: every context gets the default. These rules are kept, and can be edited, but
              aren&rsquo;t applied until you turn targeting back on.
            </Alert>
          )}

          {rules.map((rule, i) => {
            const key = ruleKeys[i];
            return (
              <RuleEditor
                key={key}
                index={i}
                count={rules.length}
                rule={rule}
                type={draft.type}
                catalog={catalog}
                attributes={attributes}
                onChange={(r) =>
                  setRules(
                    rules.map((x, j) => (j === i ? r : x)),
                    ruleKeys
                  )
                }
                onMove={(delta) => {
                  const next = [...rules];
                  const keys = [...ruleKeys];
                  [next[i], next[i + delta]] = [next[i + delta], next[i]];
                  [keys[i], keys[i + delta]] = [keys[i + delta], keys[i]];
                  setRules(next, keys);
                }}
                onRemove={() => {
                  setRules(
                    rules.filter((_, j) => j !== i),
                    ruleKeys.filter((_, j) => j !== i)
                  );
                  // A removed rule's invalid input no longer blocks review.
                  setValidity(key)(true);
                }}
                onValidityChange={setValidity(key)}
              />
            );
          })}

          <div>
            <Button
              size="sm"
              color="light"
              onClick={() => setRules([...rules, { clauses: [], value: draft.value }], [...ruleKeys, newRuleKey()])}
            >
              <HiPlus className="mr-1" /> Add rule
            </Button>
          </div>

          <div>
            <Label htmlFor="flag-default">Default (when no rule applies)</Label>
            <ValueInput
              id="flag-default"
              type={draft.type}
              value={draft.value}
              onChange={(value) => setDraft({ ...draft, value })}
              onValidityChange={setValidity('default')}
            />
          </div>

          <div className="flex items-start gap-2">
            <Checkbox
              id="flag-paused"
              className="mt-0.5"
              aria-describedby="flag-paused-help"
              checked={paused}
              onChange={(e) => {
                setPaused(e.target.checked);
                setDraft(withRules(draft, rules, e.target.checked));
              }}
            />
            <div className="flex flex-col">
              <Label htmlFor="flag-paused">Turn targeting off</Label>
              <p id="flag-paused-help" className="text-sm text-gray-600 dark:text-gray-400">
                Every context gets the default, and the rules are kept for when you turn targeting back on. Chambers
                below that override <span className="font-mono">{name}</span> aren&rsquo;t affected unless it&rsquo;s
                sealed.
              </p>
            </div>
          </div>

          <div className="flex items-start gap-2">
            <Checkbox
              id="flag-sealed"
              className="mt-0.5"
              aria-describedby="flag-sealed-help"
              checked={draft.sealed === true}
              onChange={(e) => setDraft(withSealed(draft, e.target.checked))}
            />
            <div className="flex flex-col">
              <Label htmlFor="flag-sealed">Seal</Label>
              <p id="flag-sealed-help" className="text-sm text-gray-600 dark:text-gray-400">
                Chambers below this one can&rsquo;t override <span className="font-mono">{name}</span>. Definitions they
                saved earlier stop being served.
              </p>
            </div>
          </div>

          {showProblems && problems.length > 0 && (
            <Alert color="failure">
              <ul className="list-disc pl-4">
                {problems.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            </Alert>
          )}
        </div>
      </ModalBody>
      <ModalFooter className="justify-end">
        <Button color="light" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          disabled={invalid.size > 0}
          onClick={() => {
            if (problems.length > 0) {
              setShowProblems(true);
              return;
            }
            onReview(draft);
          }}
        >
          Review changes
        </Button>
      </ModalFooter>
    </Modal>
  );
};
