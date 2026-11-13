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
 const [error,setError]=useState("");
 const [detail,setDetail]=useState<DesktopRun|null>(null);
 const [form,setForm]=useState({project_id:"",source_sha:"",artifact_sha256:"",platform:"windows-amd64"});
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
  setBusy(true);
  try{
   await api.createTestRun({...form,scenario:"desktop-observe",scenario_version:"1",required_checks:["desktop_observed"]});
   notify("观察任务已排队");await load();
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
  <div className="notice">当前可创建桌面观察任务，用于确认应用可读取和截图。通过观察不代表安装、编码或升级验收通过。</div>
  {error&&<div role="alert" className="notice">{error}</div>}
  <div className="card">
   <h3>新建桌面观察</h3>
   <label>项目 <select aria-label="项目" value={form.project_id} onChange={e=>setForm({...form,project_id:e.target.value})}>
    <option value="">请选择项目</option>{projects.map(p=><option key={p.id} value={p.id}>{p.name}</option>)}
   </select></label>
   <label>平台 <select aria-label="平台" value={form.platform} onChange={e=>setForm({...form,platform:e.target.value})}>
    {["windows-amd64","darwin-arm64","darwin-amd64","linux-amd64"].map(p=><option key={p}>{p}</option>)}
   </select></label>
   <label>源码 SHA <input aria-label="源码 SHA" value={form.source_sha} onChange={e=>setForm({...form,source_sha:e.target.value.trim()})}/></label>
   <label>制品 SHA-256 <input aria-label="制品 SHA-256" value={form.artifact_sha256} onChange={e=>setForm({...form,artifact_sha256:e.target.value.trim()})}/></label>
   <button disabled={busy} onClick={()=>void create()}>创建观察任务</button>
   <button disabled={busy} onClick={()=>void load()}>刷新</button>
  </div>
  <div className="card"><h3>测试机</h3>
   <table><thead><tr><th>机器</th><th>平台</th><th>状态</th><th>最近心跳</th><th>操作</th></tr></thead><tbody>
    {machines.map(m=><tr key={m.id}><td>{m.id}</td><td>{m.platform}</td>
     <td>{!m.enabled?"已停用":m.run_id?(m.lease_until&&Date.parse(m.lease_until)<Date.now()?"租约过期，待恢复":"执行中"):"空闲（连接状态未确认）"}</td>
     <td>{m.last_seen?new Date(m.last_seen).toLocaleString():"尚未连接"}</td>
     <td><button disabled={busy||(!m.enabled&&!!m.run_id)} onClick={()=>void setEnabled(m)}>{m.enabled?"停用机器":m.run_id?"等待清理确认":"启用机器"}</button></td></tr>)}
   </tbody></table>{!machines.length&&<p>尚未登记测试机。由管理员部署 Runner 后登记机器身份。</p>}
  </div>
  <div className="card"><h3>最近 100 次运行</h3>
   <table><thead><tr><th>运行</th><th>场景</th><th>平台</th><th>状态</th><th>操作</th></tr></thead><tbody>
    {runs.map(r=><tr key={r.id}><td>{r.id}</td><td>{r.spec.scenario}</td><td>{r.spec.platform}</td><td>{r.status}</td>
     <td><button onClick={()=>setDetail(r)}>详情</button>{["QUEUED","RUNNING"].includes(r.status)&&<button disabled={busy} onClick={()=>void cancel(r.id)}>取消</button>}</td></tr>)}
   </tbody></table>{!runs.length&&<p>暂无测试运行。</p>}
  </div>
  {detail&&<div className="card"><h3>{detail.id}</h3>
   <p>源码：{detail.spec.source_sha}</p><p>制品：{detail.spec.artifact_sha256}</p>
   <p>场景：{detail.spec.scenario} / {detail.spec.scenario_version}</p>
   {detail.report?.checks.map(c=><p key={c.id}>{c.id}：{c.status} {c.evidence&&<button onClick={()=>void download(detail.id,c.evidence)}>下载证据</button>}</p>)}
   {!detail.report&&<p>尚未提交报告。</p>}
  </div>}
 </>;
}
