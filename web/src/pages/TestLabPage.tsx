import { useCallback, useEffect, useState } from "react";
import { api, getToken } from "../api";
import type { Notify } from "../App";
import type { DesktopMachine, DesktopRun } from "../testlab-types";
import type { Project } from "../types";

export function TestLabPage({notify}:{notify:Notify}){
 const [machines,setMachines]=useState<DesktopMachine[]>([]);
 const [runs,setRuns]=useState<DesktopRun[]>([]);
 const [projects,setProjects]=useState<Project[]>([]);
 const [busy,setBusy]=useState(false);
 const [recovery,setRecovery]=useState<DesktopMachine|null>(null);
 const [cleanupConfirmed,setCleanupConfirmed]=useState(false);
 const [recoveryReason,setRecoveryReason]=useState("");
 const [error,setError]=useState("");
 const [detail,setDetail]=useState<DesktopRun|null>(null);
 const [form,setForm]=useState({project_id:"",source_sha:"",artifact_sha256:"",platform:"windows-amd64",scenario:"desktop-observe",scenario_version:"1",checks:"desktop_observed"});
 const load=useCallback(async()=>{
  setBusy(true);
  try{
   const [m,r,p]=await Promise.all([api.listTestMachines(),api.listTestRuns(),api.listProjects()]);
   setMachines(m);setRuns(r);setProjects(p);setError("");
   setDetail(old=>old?r.find(item=>item.id===old.id)??null:null);
  }catch(e){setError(e instanceof Error?e.message:"读取失败");}
  finally{setBusy(false);}
 },[]);
 useEffect(()=>{void load();},[load]);
 async function create(){
  if(!form.project_id||!/^([a-f0-9]{40}|[a-f0-9]{64})$/.test(form.source_sha)||! /^[a-f0-9]{64}$/.test(form.artifact_sha256)){
   notify("请选择项目，并填写完整源码 SHA 和安装包 SHA-256","error");return;
  }
  const required_checks=form.checks.split(/[\s,，]+/).filter(Boolean);
  if(!form.scenario.trim()||!form.scenario_version.trim()||!required_checks.length||new Set(required_checks).size!==required_checks.length){
   notify("请填写场景、版本和不重复的必需检查项","error");return;
  }
  setBusy(true);
  try{
   const {checks,...spec}=form;
   await api.createTestRun({...spec,scenario:form.scenario.trim(),scenario_version:form.scenario_version.trim(),required_checks});
   notify("测试任务已排队；等待支持该场景的 Runner 领取");await load();
  }catch(e){notify(e instanceof Error?e.message:"创建失败","error");}
  finally{setBusy(false);}
 }
 async function cancel(id:string){
  setBusy(true);
  try{await api.cancelTestRun(id);notify("已请求停止；机器需确认清理后释放");await load();}
  catch(e){notify(e instanceof Error?e.message:"取消失败","error");}
  finally{setBusy(false);}
 }
 async function setEnabled(machine:DesktopMachine){
  setBusy(true);
  try{await api.setTestMachineEnabled(machine.id,!machine.enabled);notify(machine.enabled?"机器已停用；执行中的任务会停止，仍需检查本地清理":"机器已启用");await load();}
  catch(e){notify(e instanceof Error?e.message:"操作失败","error");}
  finally{setBusy(false);}
 }
 async function recover(){
  if(!recovery||!cleanupConfirmed||recoveryReason.trim().length<3)return;
  setBusy(true);
  try{
   await api.recoverTestMachine(recovery.id,recovery.generation,recoveryReason.trim());
   setRecovery(null);setCleanupConfirmed(false);setRecoveryReason("");
   notify("占用已解除，机器仍停用；可按需重新启用");await load();
  }catch(e){notify(e instanceof Error?e.message:"恢复失败，请刷新机器状态","error");}
  finally{setBusy(false);}
 }
 async function download(runID:string,reference:string){
  if(!/^sha256:[a-f0-9]{64}$/.test(reference)){notify("此证据尚未上传","error");return;}
  try{
   const headers:Record<string,string>={};const token=getToken();if(token)headers.Authorization="Bearer "+token;
   const res=await fetch("/admin/test-runs/"+encodeURIComponent(runID)+"/evidence/"+reference.slice(7),{headers,credentials:"same-origin"});
   if(!res.ok)throw new Error("证据下载失败（"+res.status+"）");
   const url=URL.createObjectURL(await res.blob());const link=document.createElement("a");
   link.href=url;link.download=reference.slice(7);link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
  }catch(e){notify(e instanceof Error?e.message:"下载失败","error");}
 }
 return <>
  <div className="notice">场景名称和版本必须与测试机已部署配置一致。桌面观察仅用于读取和截图，不满足发布验收门禁。</div>
  {error&&<div role="alert" className="notice">{error}</div>}
  <div className="card">
   <h3>新建桌面测试</h3>
   <label>项目 <select aria-label="项目" value={form.project_id} onChange={e=>setForm({...form,project_id:e.target.value})}>
    <option value="">请选择项目</option>{projects.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}
   </select></label>
   <label>平台 <select aria-label="平台" value={form.platform} onChange={e=>setForm({...form,platform:e.target.value})}>
    {["windows-amd64","darwin-arm64","darwin-amd64","linux-amd64"].map(p=><option key={p}>{p}</option>)}
   </select></label>
   <label>源码 SHA <input aria-label="源码 SHA" value={form.source_sha} onChange={e=>setForm({...form,source_sha:e.target.value.trim()})}/></label>
   <label>制品 SHA-256 <input aria-label="制品 SHA-256" value={form.artifact_sha256} onChange={e=>setForm({...form,artifact_sha256:e.target.value.trim()})}/></label>
   <label>场景 <input aria-label="场景" value={form.scenario} onChange={e=>setForm({...form,scenario:e.target.value})}/></label>
   <label>场景版本 <input aria-label="场景版本" value={form.scenario_version} onChange={e=>setForm({...form,scenario_version:e.target.value})}/></label>
   <label>必需检查项 <input aria-label="必需检查项" placeholder="以逗号或空格分隔" value={form.checks} onChange={e=>setForm({...form,checks:e.target.value})}/></label>
   <p>这些检查项必须全部通过并上传证据；填写名称不会自动安装测试场景。</p>
   <button disabled={busy} onClick={()=>void create()}>创建测试任务</button>
   <button disabled={busy} onClick={()=>void load()}>刷新</button>
  </div>
  <div className="card"><h3>测试机</h3>
   <table><thead><tr><th>机器</th><th>平台</th><th>状态</th><th>最近心跳</th><th>操作</th></tr></thead><tbody>
    {machines.map(m=><tr key={m.id}><td>{m.id}</td><td>{m.platform}</td>
     <td>{!m.enabled?"已停用":m.run_id?(m.lease_until&&Date.parse(m.lease_until)<Date.now()?"租约过期，待恢复":"执行中"):"空闲（连接状态未确认）"}</td>
     <td>{m.last_seen?new Date(m.last_seen).toLocaleString():"尚未连接"}</td>
     <td><button disabled={busy||(!m.enabled&&!!m.run_id)} onClick={()=>void setEnabled(m)}>{m.enabled?"停用机器":m.run_id?"等待清理确认":"启用机器"}</button>{!m.enabled&&!!m.run_id&&<button disabled={busy} onClick={()=>{setRecovery({...m});setCleanupConfirmed(false);setRecoveryReason("");}}>确认清理并解除占用</button>}</td></tr>)}
   </tbody></table>{!machines.length&&<p>尚未登记测试机。由管理员部署 Runner 后登记机器身份。</p>}
  </div>
  {recovery&&<div className="card" role="region" aria-label="测试机恢复">
   <h3>解除 {recovery.id} 的占用</h3>
   <p>任务：{recovery.run_id}。请先在测试机确认 Runner 和测试子进程已停止、测试资源已清理。此操作只解除占用，不会执行本机清理。</p>
   <label><input type="checkbox" checked={cleanupConfirmed} onChange={e=>setCleanupConfirmed(e.target.checked)}/>我已确认本机清理完成</label>
   <label>清理说明 <input aria-label="清理说明" value={recoveryReason} onChange={e=>setRecoveryReason(e.target.value)} maxLength={1000}/></label>
   <button disabled={busy||!cleanupConfirmed||recoveryReason.trim().length<3} onClick={()=>void recover()}>解除占用，保持停用</button>
   <button disabled={busy} onClick={()=>setRecovery(null)}>取消</button>
  </div>}
  <div className="card"><h3>最近 100 次运行</h3>
   <table><thead><tr><th>运行</th><th>场景</th><th>平台</th><th>状态</th><th>操作</th></tr></thead><tbody>
    {runs.map(r=><tr key={r.id}><td>{r.id}</td><td>{r.spec.scenario}</td><td>{r.spec.platform}</td><td>{r.status}</td>
     <td><button onClick={()=>setDetail(r)}>详情</button>{["QUEUED","RUNNING"].includes(r.status)&&<button disabled={busy} onClick={()=>void cancel(r.id)}>取消</button>}</td></tr>)}
   </tbody></table>{!runs.length&&<p>暂无测试运行。</p>}
  </div>
  {detail&&<div className="card"><h3>{detail.id}</h3>
   <p>源码：{detail.spec.source_sha}</p><p>制品：{detail.spec.artifact_sha256}</p>
   <p>场景：{detail.spec.scenario} / {detail.spec.scenario_version}</p>
   <p>运行状态：{detail.status} · 创建时间：{new Date(detail.created_at).toLocaleString()}{detail.finished_at&&<> · 完成时间：{new Date(detail.finished_at).toLocaleString()}</>}</p>
   <h4>必需检查项</h4>
   <table><thead><tr><th>检查项</th><th>结果</th><th>证据</th></tr></thead><tbody>
    {detail.spec.required_checks.map(id=>{
     const check=detail.report?.checks.find(c=>c.id===id);
     const uploaded=!!check&&/^sha256:[a-f0-9]{64}$/.test(check.evidence);
     return <tr key={id}><td>{id}</td><td>{check?.status??"未返回"}</td><td>{uploaded?<button onClick={()=>void download(detail.id,check!.evidence)}>下载证据</button>:"无已上传证据"}</td></tr>;
    })}
   </tbody></table>
   {detail.report?.checks.filter(c=>!detail.spec.required_checks.includes(c.id)).map(c=><p key={c.id}>附加检查 {c.id}：{c.status} {/^sha256:[a-f0-9]{64}$/.test(c.evidence)&&<button onClick={()=>void download(detail.id,c.evidence)}>下载证据</button>}</p>)}
   {detail.spec.scenario==="desktop-observe"&&<p>此结果仅用于桌面观察，不满足发布验收门禁。</p>}
   {!detail.report&&<p>尚未提交报告。</p>}
  </div>}
 </>;
}
