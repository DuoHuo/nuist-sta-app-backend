"""Verify Mingde floor APIs, room bindings, and end-to-end route semantics."""
import argparse
import json
from pathlib import Path
import requests


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--base-url',default='http://127.0.0.1:8080')
    args=parser.parse_args()
    base=args.base_url.rstrip('/')+'/api/v1'
    session=requests.Session();session.trust_env=False
    def get(path):
        r=session.get(base+path,timeout=20);r.raise_for_status();return r.json()
    def route(origin,destination,accessible=False):
        r=session.post(base+'/route',json={'origin':origin,'destination':destination,'options':{'accessible_only':accessible}},timeout=30)
        return r
    building=get('/buildings/OSM-Way862952692')['data']
    assert building['has_indoor_map']
    assert [f['display_name'] for f in building['floors']]==[f'{i}F' for i in range(1,8)]
    counts=[];rooms={}
    for floor in building['floors']:
        data=get(f"/floors/{floor['floor_id']}/features")
        features=data['features'];items=[f for f in features if f['properties']['kind']=='room']
        for f in features:
            p=f['properties']
            if p['kind'] in ('room','facility'):
                assert p.get('nav_node_id') and p.get('poi_id'),p
            if p['kind']=='wall':assert p['height_m']==2.8 and p['base_m']==0
        rooms.update({f['properties']['room_code']:f['properties'] for f in items})
        counts.append({'floor':floor['display_name'],'features':len(features),'rooms':len(items)})
    for code in ['N115','N212-215','S506-507','S603-604','S601-602','N701','N702']:assert code in rooms,code
    assert not any(c.startswith('S7') for c in rooms)
    assert len(rooms)==60
    cases=[]
    for name,origin,dest,changes in [
        ('same_floor',{'poi_id':rooms['N115']['poi_id']},{'poi_id':rooms['S103']['poi_id']},0),
        ('first_to_seventh',{'poi_id':rooms['S103']['poi_id']},{'poi_id':rooms['N701']['poi_id']},6),
        ('seventh_to_first',{'poi_id':rooms['N702']['poi_id']},{'poi_id':rooms['S101']['poi_id']},6),
        ('outdoor_to_seventh',{'lng':118.7132589585629,'lat':32.20656418308891},{'poi_id':rooms['N701']['poi_id']},6)]:
        r=route(origin,dest);r.raise_for_status();data=r.json()['data']
        cross=[s for s in data['segments'] if s['type']=='floor_change']
        assert data['total_length_m']>0,(name,'zero total')
        assert abs(data['total_length_m']-sum(s['length_m'] for s in data['segments']))<.5,(name,'segment total mismatch')
        assert len(cross)==changes,(name,len(cross))
        for s in cross:assert s['from_floor_name']!=s['to_floor_name'],s
        if name=='first_to_seventh':assert [(s['from_floor_name'],s['to_floor_name']) for s in cross]==[(f'{i}F',f'{i+1}F') for i in range(1,7)]
        if name=='seventh_to_first':assert [(s['from_floor_name'],s['to_floor_name']) for s in cross]==[(f'{i}F',f'{i-1}F') for i in range(7,1,-1)]
        cases.append({'name':name,'length_m':data['total_length_m'],'floor_changes':len(cross),'steps':data['steps']})
    r=route({'poi_id':rooms['S103']['poi_id']},{'poi_id':rooms['N701']['poi_id']},True)
    assert r.status_code==404 or (r.status_code==200 and r.json().get('code')!='ok'),r.text
    report={'base_url':args.base_url,'floor_counts':counts,'rooms':len(rooms),'routes':cases,'accessible_cross_floor_rejected':True}
    output=Path(__file__).resolve().parents[1]/'data'/'mingde'/'api-validation.json'
    output.write_text(json.dumps(report,ensure_ascii=False,indent=2),encoding='utf-8')
    print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__=='__main__':main()
