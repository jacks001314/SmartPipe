export interface DesktopSpec {
 project_id:string;source_sha:string;artifact_sha256:string;scenario:string;
 scenario_version:string;platform:string;required_checks:string[];
}
export interface DesktopRun {
 id:string;spec:DesktopSpec;status:string;created_at:string;finished_at?:string;
 report?:{checks:Array<{id:string;status:string;evidence:string}>};
}
export interface DesktopMachine {
 id:string;platform:string;enabled:boolean;generation:number;run_id:string;
 lease_until?:string;last_seen?:string;
}

export type TestMode = "story" | "bug" | "explore" | "hotswap" | "gate";

export interface EngineInputSpec {
 name:string;kind:"workspace"|"artifacts"|"stories"|"hotswap"|"file";
 sha256:string;bytes?:number;unpack?:"tar.gz"|"zip"|"none";target?:string;
}

export interface EngineSpec {
 inputs?:EngineInputSpec[];
 stories?:Array<{input:string;path:string;title?:string;required_checks?:string[]}>;
 hotswap?:{input:string;path:string;restore_policy?:"always"|"on_failure"|"never"};
 agent?:{command?:string;model?:string;sandbox?:string;args?:string[];extra_args?:string[];timeout_seconds?:number};
 env?:Record<string,string>;
 workdir?:string;
}

export interface TestStory {
 id:string;project_id:string;slug:string;title:string;body:string;mode:TestMode;
 environment:string;platform:string;required_checks:string[];tags:string[];
 events:TestStoryEvent[];
 timeout_seconds:number;created_at?:string;updated_at?:string;
}

export type StoryTriggerKind = "ui" | "shell" | "http" | "manual";

export interface TestStoryEvent {
 id:string;title:string;trigger?:string;trigger_kind?:StoryTriggerKind;
 method:string;expected?:string;
}

export interface SessionCheck {
 id:string;status:"PASS"|"FAIL"|"BLOCKED"|"SKIPPED";evidence?:string;note?:string;updated_at?:string;
}

export interface SessionFinding {
 id:string;severity:"blocker"|"critical"|"major"|"minor"|"info";
 status?:string;title:string;detail?:string;repro?:string;expected?:string;actual?:string;
 evidence?:string[];check_ref?:string;
}

export interface SessionSpec {
 project_id:string;title:string;kind?:string;mode?:TestMode;environment:string;platform:string;
 story_ref?:string;story?:string;required_checks?:string[];events?:TestStoryEvent[];
 timeout_seconds:number;engine?:EngineSpec;
}

export interface TestSession {
 id:string;spec:SessionSpec;status:string;machine_id?:string;verdict?:string;summary?:string;
 checks?:SessionCheck[];findings?:SessionFinding[];
 created_at:string;started_at?:string;finished_at?:string;deadline?:string;
}

export interface TestSessionInput {
 name:string;kind:string;sha256:string;mime_type:string;bytes:number;
}

export interface SessionEvent {
 seq:number;kind:string;message:string;at:string;
}

export interface SessionEvidence {
 sha256:string;mime_type:string;bytes:number;
}
