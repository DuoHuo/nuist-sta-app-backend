"""Generate Mingde test GLBs, GeoJSON and an additive PostGIS import.

Requires numpy, shapely, trimesh, mapbox-earcut. All lengths are estimates.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import math
from pathlib import Path

import numpy as np
import trimesh
from shapely.geometry import Polygon, Point, LineString, box, mapping
from shapely.ops import unary_union, transform

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / 'data' / 'mingde'
BUILDING = 'OSM-Way862952692'
SOURCE = 'mingde-plans-v1'
# Pose fitted to the OSM outline (way/862952692) by scripts/align_mingde.py.
ORIGIN = (118.71294883038847, 32.20703416673561)
ANGLE = math.radians(11.266947222244033)
HEIGHT = 3.6
COLORS = {'room': [181, 211, 219, 255], 'corridor': [235, 224, 201, 255],
          'toilet': [151, 200, 189, 255], 'stair': [219, 176, 112, 255],
          'roof': [167, 183, 177, 255], 'wall': [234, 237, 232, 255],
          'door': [110, 151, 162, 255], 'column': [206, 213, 210, 255]}
PHOTOS = ['cf64b0a1386b71d25a582e06ea3a5706.jpg', 'ea1f696a5400e183bfbc393c3ce34927.jpg',
          '65dd1f5562417c9525f04b9dd09804af.jpg', '032c60e018513d31d0c4ca995825906f.jpg',
          '13ffde6cdabb73178b67238d49246370.jpg', '05fb11495cbb866ca7ae22974424d658.jpg',
          '22349e8584ade738f4fbc6532126cfe3.jpg']


def wgs(x, z, _=None):
    lat = math.radians(ORIGIN[1])
    mx = 111412.84 * math.cos(lat) - 93.5 * math.cos(3*lat) + .118 * math.cos(5*lat)
    my = 111132.92 - 559.82 * math.cos(2*lat) + 1.175 * math.cos(4*lat)
    return (ORIGIN[0] + (x*math.cos(ANGLE)+z*math.sin(ANGLE))/mx,
            ORIGIN[1] + (x*math.sin(ANGLE)-z*math.cos(ANGLE))/my)


def geo(poly):
    return mapping(transform(wgs, poly))


def parts(g):
    return list(g.geoms) if hasattr(g, 'geoms') else [g]


def curved_room():
    arc = [(50.5+10.5*math.cos(a), 9-9*math.sin(a)) for a in np.linspace(math.pi/2, 0, 22)]
    return Polygon([(50.5, 9), (50.5, 0), *arc[1:], (50.5, 9)])


def lower_east():
    return unary_union([box(44, 11, 61, 21), Point(61, 12.5).buffer(1.3, quad_segs=12)] )


def build_floor(n):
    zones, doors, nodes, edges = [], [], {}, []

    def node(key, xy, kind='junction'):
        nodes[key] = {'key': key, 'xy': xy, 'kind': kind}
        return key

    def edge(a, b, kind='corridor', accessible=True):
        if a != b and not any({e['source'],e['target']} == {a,b} for e in edges):
            edges.append({'source': a, 'target': b, 'kind': kind, 'accessible': accessible})

    def zone(key, name, poly, kind='room', category='classroom', room_code=None):
        zones.append({'key': key, 'name': name, 'poly': poly, 'kind': kind,
                      'category': category, 'room_code': room_code, 'node': None})
        return zones[-1]

    def door(z, xy, axis, dest, width=1.5):
        key = f"{z['key']}-door-{sum(d['zone']==z['key'] for d in doors)+1}"
        cut = box(xy[0]-.3, xy[1]-width/2, xy[0]+.3, xy[1]+width/2) if axis=='v' else box(xy[0]-width/2,xy[1]-.3,xy[0]+width/2,xy[1]+.3)
        doors.append({'key':key,'zone':z['key'],'xy':xy,'poly':cut})
        node(key,xy,'room_door')
        edge(key,dest,'door',z['category'] not in ('stair','roof'))
        if z['node'] is None:
            z['node']=key
        return key

    north_x = [16,18.3,28.2,30.8,42.6,47.25,51.7,59.7] if n < 7 else [45.3,47.25,51.7,59.7]
    for x in north_x: node(f'north-{x}',(x,10))
    for a,b in zip(north_x,north_x[1:]): edge(f'north-{a}',f'north-{b}')
    zone('north-corridor','北侧走廊',box(17 if n<7 else 44,9,61,11),'corridor','corridor')
    if n < 7:
        spine_y=[1.5,10,19.8,25.5,31.5,33.5]
        for y in spine_y: node(f'spine-{y}',(16,y))
        for a,b in zip(spine_y,spine_y[1:]): edge(f'spine-{a}',f'spine-{b}')
        edge('north-16','spine-10')
        zone('spine','南北连接走廊',box(15,0,17,34.5),'corridor','corridor')
        sx=[1.5,13.6,16,18.2,20.75,28,30.8,42.6,45.75]
        for x in sx: node(f'south-{x}',(x,33.5))
        for a,b in zip(sx,sx[1:]): edge(f'south-{a}',f'south-{b}')
        edge('south-16','spine-33.5')
        zone('south-corridor','南侧走廊',box(0,32.5,47.5,34.5),'corridor','corridor')
        hall_code='N212-215' if n==2 else f'N{n}15'
        hall=zone(hall_code,hall_code,box(0,0,15,19),room_code=hall_code)
        dh=door(hall,(15,10),'v','spine-10',1.8)
        door(hall,(15,1.5),'v','spine-1.5')
        node('hall-west',(1,10)); edge(dh,'hall-west')
        for code,rect,xs,side in [(f'N{n}08-{n}10',(17,0,29.5,9),[18.3,28.2],9),
                                  (f'N{n}04-{n}06',(29.5,0,44,9),[30.8,42.6],9),
                                  (f'N{n}09-{n}11',(17,11,29.5,19),[18.3,28.2],11),
                                  (f'N{n}05-{n}07',(29.5,11,44,19),[30.8,42.6],11)]:
            z=zone(code,code,box(*rect),room_code=code)
            for x in xs: door(z,(x,side),'h',f'north-{x}')
        for i,(za,zb,dy) in enumerate([(19,26.5,25.5),(26.5,32.5,31.5)],1):
            z=zone(f'toilet-{i}',f'{n}F 卫生间{i}（性别未标注）',box(7.5,za,15,zb),'facility','toilet')
            door(z,(15,dy),'v',f'spine-{dy}',1.2)
        if n==1:
            specs=[('S103',(0,34.5,15,45),[1.5,13.6]),('S101',(29.5,34.5,41.5,45),[30.8])]
            lobby=zone('lobby','一层入口大厅',box(15,34.5,29.5,43.5),'corridor','lobby')
            node('lobby',(22,39)); edge('south-20.75','lobby')
            node('entrance',(22,43.5),'entrance'); edge('lobby','entrance')
            doors.append({'key':'main-entrance','zone':'lobby','xy':(22,43.5),'poly':box(19.5,43.2,24.5,43.8)})
            zone('south-service','一层南侧未标注用房',box(41.5,34.5,44,45),'facility','service')
            door(zones[-1],(42.6,34.5),'h','south-42.6')
        elif n==6:
            specs=[('S603-604',(0,34.5,15,45),[1.5,13.6]),('S601-602',(15,34.5,29.5,45),[18.2,28])]
            z=zone('roof','六层屋面（测试区域）',box(29.5,34.5,44,45),'facility','roof')
            door(z,(30.8,34.5),'h','south-30.8')
        else:
            left=f'S{n}06-{n}07' if n==5 else f'S{n}05-{n}06'
            specs=[(left,(0,34.5,15,45),[1.5,13.6]),(f'S{n}03-{n}04',(15,34.5,29.5,45),[18.2,28]),(f'S{n}01-{n}02',(29.5,34.5,44,45),[30.8,42.6])]
        for code,rect,xs in specs:
            z=zone(code,code,box(*rect),room_code=code)
            for x in xs: door(z,(x,34.5),'h',f'south-{x}')
        for key,name,poly,xy,axis,dest,center in [
            ('west','西侧楼梯',unary_union([box(-3.5,8,0,13.5),Point(-1.75,13.5).buffer(1.75)]),(0,10),'v','hall-west',(-1.75,10)),
            ('central','中部楼梯',box(17,26.5,24.5,32.5),(20.75,32.5),'h','south-20.75',(20.75,30.8)),
            ('southeast','东南楼梯',unary_union([box(44,34.5,47.5,38.5),Point(45.75,38.5).buffer(1.75)]),(45.75,34.5),'h','south-45.75',(45.75,36))]:
            z=zone('stairs-'+key,name,poly,'facility','stair')
            d=door(z,xy,axis,dest,1.6); node('stairs-'+key,center,'stair');edge(d,'stairs-'+key,'stair',False)
            z['node']='stairs-'+key
    east_code=f'N{n}02'
    z=zone(east_code,east_code,curved_room(),room_code=east_code)
    door(z,(51.7,9),'h','north-51.7')
    lower_code='N701' if n==7 else f'N{n}01-{n}03'
    z=zone(lower_code,lower_code,lower_east(),room_code=lower_code)
    for x in ([45.3,59.7] if n==7 else [47.25,59.7]):door(z,(x,11),'h',f'north-{x}')
    stair=zone('stairs-northeast','东北楼梯（通至七层）',unary_union([box(44,0,50.5,9),Point(48.8,0).buffer(1.4)]),'facility','stair')
    d=door(stair,(47.25,9),'h','north-47.25',2.5)
    node('stairs-northeast',(47.25,7.8),'stair');edge(d,'stairs-northeast','stair',False);stair['node']='stairs-northeast'
    if n==7:
        doors.append({'key':'west-unverified','zone':'north-corridor','xy':(44,10),'poly':box(43.7,9.3,44.3,10.7)})
    corridors=unary_union([z['poly'] for z in zones if z['kind']=='corridor'])
    boundary=unary_union([z['poly'].boundary for z in zones if z['kind']!='corridor']+[corridors.boundary])
    walls=boundary.buffer(.10,join_style=2).difference(unary_union([d['poly'] for d in doors]))
    # The entrance lobby shares an open edge with the first-floor south corridor.
    if n==1: walls=walls.difference(box(15.1,34.2,29.4,34.8))
    return {'floor':n,'zones':zones,'doors':doors,'nodes':nodes,'edges':edges,'walls':walls}


def mesh(poly, base, height, color):
    m=trimesh.creation.extrude_polygon(poly,height,engine='earcut')
    xy=m.vertices.copy()
    m.vertices=np.column_stack((xy[:,0],xy[:,2]+base,xy[:,1]))
    m.faces=m.faces[:,::-1]
    m.merge_vertices(digits_vertex=5)
    m.update_faces(m.nondegenerate_faces())
    m.visual=trimesh.visual.ColorVisuals(mesh=m,face_colors=color)
    return m


def add_floor(scene,floor,offset):
    group=f"floor_{floor['floor']-1}"
    scene.graph.update(frame_to=group,frame_from='world',matrix=trimesh.transformations.translation_matrix([0,offset,0]))
    for z in floor['zones']:
        color=COLORS.get(z['category'],COLORS.get(z['kind'],COLORS['room']))
        for i,p in enumerate(parts(z['poly'])):
            m=mesh(p,-.18,.18,color)
            m.metadata={'name':z['name'],'room_code':z['room_code'],'kind':z['kind'],'category':z['category'],'level_index':floor['floor']-1,'source':SOURCE}
            scene.add_geometry(m,node_name=f"{group}_{z['key']}_{i}",parent_node_name=group,metadata=m.metadata)
    for i,p in enumerate(parts(floor['walls'])):
        if p.area<.001:continue
        scene.add_geometry(mesh(p,0,2.8,COLORS['wall']),node_name=f'{group}_wall_{i}',parent_node_name=group)
    for z in floor['zones']:
        if z['category']!='stair':continue
        x0,y0,x1,y1=z['poly'].bounds
        for step in range(10):
            zz=y0+.8+step*.27
            tread=box(x0+.25,zz,x1-.25,zz+.25).intersection(z['poly'])
            if tread.area>.05:
                scene.add_geometry(mesh(tread,0,.12+step*.13,COLORS['stair']),node_name=f"{group}_{z['key']}_step{step}",parent_node_name=group)
    for i,d in enumerate(floor['doors']):
        scene.add_geometry(mesh(d['poly'],.01,.025,COLORS['door']),node_name=f'{group}_door_{i}',parent_node_name=group)


def sql_text(value):
    return "'"+str(value).replace("'","''")+"'"


def json_sql(value):return sql_text(json.dumps(value,ensure_ascii=False,separators=(',',':')))+'::jsonb'
def geom_sql(poly):return 'ST_SetSRID(ST_GeomFromGeoJSON('+sql_text(json.dumps(geo(poly)))+'),4326)'


def make_sql(floors):
    s=['BEGIN;',"SELECT pg_advisory_xact_lock(hashtext('mingde-indoor-import'));",
       "DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM buildings WHERE building_id='"+BUILDING+"') THEN RAISE EXCEPTION 'Mingde building is missing'; END IF; IF EXISTS (SELECT 1 FROM floors WHERE building_id='"+BUILDING+"') THEN RAISE EXCEPTION 'Mingde already has floor data; refusing overwrite'; END IF; END $$;",
       'CREATE TEMP TABLE md_nodes(key text PRIMARY KEY,id bigint) ON COMMIT DROP;',
       'CREATE TEMP TABLE md_features(key text PRIMARY KEY,id bigint) ON COMMIT DROP;']
    fid=lambda n:f"(SELECT floor_id FROM floors WHERE building_id='{BUILDING}' AND level_index={n-1})"
    nid=lambda n,k:"(SELECT id FROM md_nodes WHERE key="+sql_text(f'{n}:{k}')+")"
    for f in floors:
        n=f['floor'];fl=fid(n)
        s.append(f"INSERT INTO floors(building_id,level_index,display_name,elevation_m,sort_order) VALUES('{BUILDING}',{n-1},'{n}F',{(n-1)*HEIGHT},{n});")
        for key,nd in f['nodes'].items():
            s.append(f"WITH q AS (INSERT INTO nav_nodes(building_id,floor_id,kind,geom,props) VALUES('{BUILDING}',{fl},{sql_text(nd['kind'])},{geom_sql(Point(nd['xy']))},{json_sql({'source':SOURCE,'key':key,'estimated':True})}) RETURNING node_id) INSERT INTO md_nodes SELECT {sql_text(str(n)+':'+key)},node_id FROM q;")
        for z in f['zones']:
            props={'source':SOURCE,'estimated':True,'local_key':z['key'],'level_index':n-1}
            props_sql=json_sql(props)
            if z['node']:props_sql+=f" || jsonb_build_object('nav_node_id',{nid(n,z['node'])})"
            s.append(f"WITH q AS (INSERT INTO indoor_features(floor_id,kind,category,name,room_code,geom,props) VALUES({fl},{sql_text(z['kind'])},{sql_text(z['category'])},{sql_text(z['name'])},{sql_text(z['room_code']) if z['room_code'] else 'NULL'},{geom_sql(z['poly'])},{props_sql}) RETURNING feature_id) INSERT INTO md_features SELECT {sql_text(str(n)+':'+z['key'])},feature_id FROM q;")
            if z['node'] and z['kind'] in ('room','facility'):
                ft="(SELECT id FROM md_features WHERE key="+sql_text(f"{n}:{z['key']}")+")"
                keywords=[z['name'],'明德楼',f'{n}F']
                s.append(f"WITH q AS (INSERT INTO pois(name,category,keywords,building_id,floor_id,indoor_feature_id,location,nav_node_id) VALUES({sql_text(z['name'])},{sql_text(z['category'])},ARRAY[{','.join(sql_text(k) for k in keywords)}],'{BUILDING}',{fl},{ft},{geom_sql(z['poly'].representative_point())},{nid(n,z['node'])}) RETURNING poi_id) UPDATE indoor_features SET props=props||jsonb_build_object('poi_id',(SELECT poi_id FROM q)) WHERE feature_id={ft};")
        for i,p in enumerate(parts(f['walls'])):
            s.append(f"INSERT INTO indoor_features(floor_id,kind,category,geom,props) VALUES({fl},'wall','wall',{geom_sql(p)},{json_sql({'height_m':2.8,'base_m':0,'source':SOURCE,'estimated':True})});")
        for d in f['doors']:
            s.append(f"INSERT INTO indoor_features(floor_id,kind,category,name,geom,props) VALUES({fl},'door','door',{sql_text(d['key'])},{geom_sql(d['poly'])},{json_sql({'source':SOURCE})});")
        for e in f['edges']:
            a=f['nodes'][e['source']]['xy']; b=f['nodes'][e['target']]['xy']
            cost=max(math.dist(a,b),.05)
            s.append(f"INSERT INTO nav_edges(source,target,cost_m,reverse_cost_m,edge_kind,name,geom,is_accessible) VALUES({nid(n,e['source'])},{nid(n,e['target'])},{cost},{cost},{sql_text(e['kind'])},'明德楼测试室内连线',{geom_sql(LineString([a,b]))},{str(e['accessible']).lower()});")
    for a,b in zip(floors,floors[1:]):
        for key,nd in a['nodes'].items():
            if nd['kind']=='stair' and key in b['nodes']:
                s.append(f"INSERT INTO nav_edges(source,target,cost_m,reverse_cost_m,edge_kind,name,is_accessible,floor_change,geom) VALUES({nid(a['floor'],key)},{nid(b['floor'],key)},10.8,10.8,'stair',{sql_text('明德楼 '+key)},false,true,{geom_sql(LineString([nd['xy'],(nd['xy'][0]+.001,nd['xy'][1])]))});")
    s.append(f"INSERT INTO building_entrances(building_id,name,is_accessible,geom,nav_node_id) VALUES('{BUILDING}','南侧主入口（位置估算，无障碍未核实）',false,{geom_sql(Point(22,43.5))},{nid(1,'entrance')});")
    s.append(f"""DO $$
DECLARE entrance bigint := {nid(1,'entrance')}; road record; anchor bigint; ep geometry; ap geometry;
BEGIN
 SELECT geom INTO ep FROM nav_nodes WHERE node_id=entrance;
 SELECT e.* INTO road FROM nav_edges e JOIN nav_nodes a ON a.node_id=e.source JOIN nav_nodes b ON b.node_id=e.target
 WHERE a.floor_id IS NULL AND b.floor_id IS NULL AND e.is_open AND e.geom IS NOT NULL
 AND e.edge_kind='walkway' AND e.reverse_cost_m>=0
 ORDER BY e.geom <-> ep LIMIT 1;
 IF road.edge_id IS NULL OR ST_Distance(ep::geography,road.geom::geography)>40 THEN
 RAISE EXCEPTION 'No outdoor walkway within 40m of estimated entrance'; END IF;
 ap := ST_ClosestPoint(road.geom,ep);
 INSERT INTO nav_nodes(kind,geom,props) VALUES('junction',ap,'{{"source":"mingde-plans-v1","estimated":true}}') RETURNING node_id INTO anchor;
 INSERT INTO nav_edges(source,target,cost_m,reverse_cost_m,edge_kind,name,geom,is_accessible)
 SELECT a,b,ST_Length(g::geography),ST_Length(g::geography),'walkway','明德楼测试入口连接',g,false
 FROM (SELECT entrance a,anchor b,ST_MakeLine(ep,ap) g UNION ALL
 SELECT anchor,road.source,ST_MakeLine(ap,n.geom) FROM nav_nodes n WHERE n.node_id=road.source UNION ALL
 SELECT anchor,road.target,ST_MakeLine(ap,n.geom) FROM nav_nodes n WHERE n.node_id=road.target) q;
END $$;""")
    s.append(f"UPDATE buildings SET has_indoor_map=true,height_m=25.2,height_source='estimated_floors',updated_at=now() WHERE building_id='{BUILDING}';")
    s.append('COMMIT;')
    return '\n'.join(s)+'\n'


def validate(floors):
    allnodes={(f['floor'],k) for f in floors for k in f['nodes']}
    adjacency={k:set() for k in allnodes}
    for f in floors:
        for z in f['zones']:
            assert z['poly'].is_valid and z['poly'].area>0,z['key']
            if z['kind'] in ('room','facility'):assert z['node'],z['key']
        walk=unary_union([z['poly'] for z in f['zones']]).buffer(.02)
        for e in f['edges']:
            a=(f['floor'],e['source']); b=(f['floor'],e['target'])
            line=LineString([f['nodes'][e['source']]['xy'],f['nodes'][e['target']]['xy']])
            assert walk.covers(line),(a,b,'outside floor')
            assert line.intersection(f['walls'].buffer(-.01)).length<.03,(a,b,'wall crossing')
            adjacency[a].add(b);adjacency[b].add(a)
    for f in floors[:-1]:
        for k,nd in f['nodes'].items():
            a=(f['floor'],k);b=(f['floor']+1,k)
            if nd['kind']=='stair' and b in adjacency:adjacency[a].add(b);adjacency[b].add(a)
    seen=set();todo=[(1,'entrance')]
    while todo:
        k=todo.pop()
        if k not in seen:seen.add(k);todo.extend(adjacency[k]-seen)
    assert len(seen)==len(allnodes),(len(seen),len(allnodes))
    return {'valid':True,'nodes':len(allnodes),'reachable_nodes':len(seen),'floor_count':len(floors),
            'rooms':sum(z['kind']=='room' for f in floors for z in f['zones']),
            'facilities':sum(z['kind']=='facility' for f in floors for z in f['zones']),
            'cross_floor_links':sum(1 for f in floors[:-1] for k,nd in f['nodes'].items() if nd['kind']=='stair' and (f['floor']+1,k) in allnodes)}


def main():
    OUT.mkdir(parents=True,exist_ok=True)
    floors=[build_floor(i) for i in range(1,8)]
    report=validate(floors)
    full=trimesh.Scene(base_frame='world')
    manifest={'schema_version':1,'coordinate_system':'building-local-meters-y-up',
              'origin':{'longitude':ORIGIN[0],'latitude':ORIGIN[1]},'rotation_deg':-math.degrees(ANGLE),
              'building_id':BUILDING,'name':'明德楼','source':SOURCE,'estimated':True,
              'notes':'消防图比例复建，非测绘；层高3.6m、墙高2.8m。局部x沿楼栋向东，y向上，z沿图向南。卫生间性别、无障碍及七层西门外区域未核实。',
              'floors':[]}
    for f in floors:
        n=f['floor'];scene=trimesh.Scene(base_frame='world')
        add_floor(scene,f,0);add_floor(full,f,(n-1)*HEIGHT)
        path=OUT/f'mingde-{n}F.glb';scene.export(str(path))
        manifest['floors'].append({'level_index':n-1,'display_name':f'{n}F','elevation_m':(n-1)*HEIGHT,'node_name':f'floor_{n-1}','file':path.name,'source_photo':PHOTOS[n-1]})
        features=[]
        for z in f['zones']:
            features.append({'type':'Feature','geometry':geo(z['poly']),'properties':{k:v for k,v in z.items() if k not in ('poly','node')}|{'level_index':n-1,'building_id':BUILDING,'estimated':True}})
        for p in parts(f['walls']):features.append({'type':'Feature','geometry':geo(p),'properties':{'kind':'wall','height_m':2.8,'base_m':0,'level_index':n-1,'building_id':BUILDING}})
        (OUT/f'mingde-{n}F.geojson').write_text(json.dumps({'type':'FeatureCollection','features':features},ensure_ascii=False,indent=2),encoding='utf-8')
    full.export(str(OUT/'mingde.glb'))
    (OUT/'manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2),encoding='utf-8')
    (OUT/'import-indoor.sql').write_text(make_sql(floors),encoding='utf-8')
    report['files']={p.name:{'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest()} for p in OUT.glob('*.glb')}
    (OUT/'validation.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
    print(json.dumps(report,ensure_ascii=False,indent=2))

if __name__=='__main__':main()
