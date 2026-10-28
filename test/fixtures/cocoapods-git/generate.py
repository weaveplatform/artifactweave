import os, subprocess, tempfile, pathlib, json
out=pathlib.Path(__file__).resolve().parent
env=os.environ|{'GIT_AUTHOR_NAME':'Fixture','GIT_AUTHOR_EMAIL':'fixture@example.invalid','GIT_COMMITTER_NAME':'Fixture','GIT_COMMITTER_EMAIL':'fixture@example.invalid','GIT_AUTHOR_DATE':'2026-01-01T00:00:00Z','GIT_COMMITTER_DATE':'2026-01-01T00:00:00Z','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null'}
def git(root,*args,data=None):
 return subprocess.check_output(['git','-C',root,*args],input=data,env=env,stderr=subprocess.DEVNULL)
def repo(root,files):
 git(root,'init','-b','main')
 for name,body in files.items():
  p=pathlib.Path(root)/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_text(body)
 git(root,'add','.');git(root,'commit','-m','fixture')
 return git(root,'rev-parse','HEAD').decode().strip()
with tempfile.TemporaryDirectory() as dep, tempfile.TemporaryDirectory() as root:
 sub=repo(dep,{'Dep.m':'void dependency(void) {}\n'})
 commit=repo(root,{'Sources/HelloPod.m':'void hello(void) {}\n','README.md':'Controlled Git fixture\n','.gitmodules':'[submodule "dependency"]\n\tpath = Vendor\n\turl = ../dep.git\n'})
 git(root,'update-index','--add','--cacheinfo','160000,'+sub+',Vendor')
 git(root,'commit','-m','Pin dependency')
 commit=git(root,'rev-parse','HEAD').decode().strip()
 git(root,'tag','-a','1.0.0','-m','Release fixture')
 tag=git(root,'rev-parse','refs/tags/1.0.0').decode().strip()
 for name,path,rev in [('root',root,tag),('submodule',dep,sub)]:
  (out/(name+'.pack')).write_bytes(git(path,'pack-objects','--stdout','--revs',data=(rev+'\n').encode()))
 (pathlib.Path(root)/'Sources/HelloPod.m').write_text('void hello_updated(void) {}\n')
 git(root,'add','.');git(root,'commit','-m','Move mutable tag')
 updated=git(root,'rev-parse','HEAD').decode().strip()
 git(root,'tag','-f','-a','1.0.0','-m','Updated release fixture')
 updated_tag=git(root,'rev-parse','refs/tags/1.0.0').decode().strip()
 (out/'updated.pack').write_bytes(git(root,'pack-objects','--stdout','--revs',data=(updated_tag+'\n').encode()))
 (out/'refs.json').write_text(json.dumps({'commit':commit,'tag':tag,'submodule':sub,'updated_commit':updated,'updated_tag':updated_tag},indent=2)+'\n')
