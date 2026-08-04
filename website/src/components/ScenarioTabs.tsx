import { useRef, useState, type KeyboardEvent } from "react";

import { scenarioById, scenarios } from "../demo/scenarios";
import {
  createDemoState,
  getAvailableEvents,
  scenarioIds,
  transitionDemo,
  type DemoEventKind,
  type DemoMachineState,
  type ScenarioId
} from "../demo/stateMachine";
import { DemoConsole } from "./DemoConsole";

function createStateMap(): Record<ScenarioId, DemoMachineState> {
  return Object.fromEntries(
    scenarioIds.map((scenarioId) => [scenarioId, createDemoState(scenarioId)])
  ) as Record<ScenarioId, DemoMachineState>;
}

function createReviewReasonMap(): Record<ScenarioId, string> {
  return Object.fromEntries(
    scenarios.map((scenario) => [scenario.id, scenario.defaultReviewReason])
  ) as Record<ScenarioId, string>;
}

export function ScenarioTabs() {
  const [activeScenario, setActiveScenario] = useState<ScenarioId>("local-guard");
  const [states, setStates] = useState<Record<ScenarioId, DemoMachineState>>(createStateMap);
  const [reviewReasons, setReviewReasons] =
    useState<Record<ScenarioId, string>>(createReviewReasonMap);
  const [feedback, setFeedback] = useState<Record<ScenarioId, string>>({
    "local-guard": "",
    "github-approval": "",
    "mcp-secret": ""
  });
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  const scenario = scenarioById[activeScenario];
  const state = states[activeScenario];

  function handleEvent(eventKind: DemoEventKind) {
    const result = transitionDemo(state, {
      type: eventKind,
      reason:
        eventKind === "APPROVE" || eventKind === "REJECT"
          ? reviewReasons[activeScenario]
          : undefined
    });

    if (!result.accepted) {
      setFeedback((current) => ({
        ...current,
        [activeScenario]: result.reason ?? "状态迁移被拒绝"
      }));
      return;
    }

    setStates((current) => ({
      ...current,
      [activeScenario]: result.state
    }));
    setFeedback((current) => ({ ...current, [activeScenario]: "" }));
  }

  function handleReset() {
    setStates((current) => ({
      ...current,
      [activeScenario]: createDemoState(activeScenario)
    }));
    setReviewReasons((current) => ({
      ...current,
      [activeScenario]: scenario.defaultReviewReason
    }));
    setFeedback((current) => ({ ...current, [activeScenario]: "" }));
  }

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let nextIndex = index;

    if (event.key === "ArrowRight") {
      nextIndex = (index + 1) % scenarios.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + scenarios.length) % scenarios.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = scenarios.length - 1;
    } else {
      return;
    }

    event.preventDefault();
    const nextScenario = scenarios[nextIndex];
    setActiveScenario(nextScenario.id);
    tabRefs.current[nextIndex]?.focus();
  }

  return (
    <div className="scenario-shell">
      <div className="scenario-tabs" role="tablist" aria-label="静态交互场景">
        {scenarios.map((item, index) => {
          const selected = item.id === activeScenario;
          return (
            <button
              aria-controls={`scenario-panel-${item.id}`}
              aria-selected={selected}
              className={selected ? "scenario-tab scenario-tab-active" : "scenario-tab"}
              id={`scenario-tab-${item.id}`}
              key={item.id}
              onClick={() => setActiveScenario(item.id)}
              onKeyDown={(event) => handleTabKeyDown(event, index)}
              ref={(node) => {
                tabRefs.current[index] = node;
              }}
              role="tab"
              tabIndex={selected ? 0 : -1}
              type="button"
            >
              <span>{item.index}</span>
              <strong>{item.shortName}</strong>
            </button>
          );
        })}
      </div>

      <div className="scenario-intro">
        <h3>{scenario.title}</h3>
        <p>{scenario.summary}</p>
      </div>

      <DemoConsole
        availableEvents={getAvailableEvents(state)}
        feedback={feedback[activeScenario]}
        onEvent={handleEvent}
        onReset={handleReset}
        onReviewReasonChange={(value) =>
          setReviewReasons((current) => ({ ...current, [activeScenario]: value }))
        }
        reviewReason={reviewReasons[activeScenario]}
        scenario={scenario}
        state={state}
      />
    </div>
  );
}
