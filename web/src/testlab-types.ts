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
