"""Upload an exported GLB package through the model API and verify hashes."""
import argparse
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import requests


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--base-url',default='http://127.0.0.1:8080')
    p.add_argument('--directory',type=Path,default=Path(__file__).resolve().parents[1]/'data'/'mingde')
    p.add_argument('--building-id',default='OSM-Way862952692')
    args=p.parse_args()
    base=args.base_url.rstrip('/')
    if base.endswith('/api/v1'):base=base[:-7]
    session=requests.Session();session.trust_env=False
    if os.environ.get('CAMPUS_COLLECT_TOKEN'):
        session.headers['X-Collect-Token']=os.environ['CAMPUS_COLLECT_TOKEN']
    manifest=json.loads((args.directory/'manifest.json').read_text(encoding='utf-8'))
    with ExitStack() as stack:
        files={'file':('mingde.glb',stack.enter_context((args.directory/'mingde.glb').open('rb')),'model/gltf-binary'),
               'manifest':('manifest.json',stack.enter_context((args.directory/'manifest.json').open('rb')),'application/json')}
        for floor in manifest['floors']:
            filename=floor['file']
            files[f"floor_{floor['level_index']}"]=(filename,stack.enter_context((args.directory/filename).open('rb')),'model/gltf-binary')
        r=session.post(base+'/api/v1/admin/buildings/'+args.building_id+'/model',files=files,timeout=120)
    if not r.ok:raise RuntimeError(f'Upload failed ({r.status_code}): {r.text[:2000]}')
    result=r.json()['data']
    current=session.get(base+'/api/v1/buildings/'+args.building_id+'/model',timeout=15)
    current.raise_for_status();data=current.json()['data']
    checked=[]
    from urllib.parse import urljoin
    for item,filename in [(data,'mingde.glb')]+[(f,manifest['floors'][i]['file']) for i,f in enumerate(data['floors'])]:
        r=session.get(urljoin(base+'/',item['url']),timeout=30);r.raise_for_status()
        expected=hashlib.sha256((args.directory/filename).read_bytes()).hexdigest()
        actual=hashlib.sha256(r.content).hexdigest()
        assert actual==expected,(filename,actual,expected)
        checked.append({'file':filename,'url':item['url'],'sha256':actual,'bytes':len(r.content)})
    report={'base_url':base,'building_id':args.building_id,'version':data['version'],'verified_files':checked}
    (args.directory/'upload-result.json').write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
    print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__=='__main__':main()
