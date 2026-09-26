//! Quote arithmetic uses integer Wh and cents. Estimated energy is distributed
//! uniformly over the requested duration; final metered settlement is separate.
use api_contracts::pricing::DevicePricing;
use common_error::{AppError,AppResult};
use serde::Deserialize;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Period {period:String,start:String,end:String,electric_price_cents:i64,service_price_cents:Option<i64>}
fn bad()->AppError {AppError::BadRequest("计费规则或预计电量/时长无效".into())}
fn minute(value:&str,end:bool)->AppResult<usize>{
 if end && value=="24:00" {return Ok(1440);}
 let b=value.as_bytes();
 if b.len()!=5 || b[2]!=b':' || ![b[0],b[1],b[3],b[4]].iter().all(u8::is_ascii_digit){return Err(bad());}
 let h=(b[0]-b'0') as usize*10+(b[1]-b'0') as usize;let m=(b[3]-b'0') as usize*10+(b[4]-b'0') as usize;
 if h>23 || m>59{return Err(bad());}Ok(h*60+m)
}
pub fn watt_hours(kwh:&str)->AppResult<i64>{
 let parts:Vec<_>=kwh.split('.').collect();
 if parts.is_empty() || parts.len()>2 || parts[0].is_empty() || parts[0].len()>3 || !parts[0].bytes().all(|v|v.is_ascii_digit()){return Err(bad());}
 let fraction=parts.get(1).copied().unwrap_or("");
 if (parts.len()==2 && fraction.is_empty()) || fraction.len()>3 || !fraction.bytes().all(|v|v.is_ascii_digit()){return Err(bad());}
 let whole:i64=parts[0].parse().map_err(|_|bad())?;
 let fract:i64=if fraction.is_empty(){0}else{fraction.parse::<i64>().map_err(|_|bad())?*10i64.pow(3-fraction.len() as u32)};
 let wh=whole*1000+fract;if !(1..=100_000).contains(&wh){return Err(bad());}Ok(wh)
}
pub(crate) fn daily_rates(rule:&DevicePricing)->AppResult<Vec<Option<(i64,i64)>>>{
 if !["kwh","minute","mixed"].contains(&rule.mode.as_str()) {return Err(bad());}
 let valid_rate=|n:i64| (0..=1_000_000).contains(&n);
 if ![rule.service_fee_cents_per_kwh,rule.service_fee_cents_per_min,rule.min_charge_cents].into_iter().all(valid_rate){return Err(bad());}
 let periods:Vec<Period>=serde_json::from_value(rule.time_of_use.clone()).map_err(|_|bad())?;
 if periods.is_empty() || periods.len()>96{return Err(bad());}
 let mut rates=vec![None;1440];
 for p in periods{
  if p.period.trim().is_empty() || !valid_rate(p.electric_price_cents) || p.service_price_cents.is_some_and(|v|!valid_rate(v)){return Err(bad());}
  let start=minute(&p.start,false)?;let end=minute(&p.end,true)?;
  if start==end{return Err(bad());}
  let duration=if end>start{end-start}else{1440-start+end};
  for offset in 0..duration{
   let index=(start+offset)%1440;
   if rates[index].is_some(){return Err(AppError::BadRequest("计费时段重叠".into()));}
   rates[index]=Some((p.electric_price_cents,p.service_price_cents.unwrap_or(rule.service_fee_cents_per_kwh)));
  }
 }
 Ok(rates)
}
pub fn estimate(rule:&DevicePricing,kwh:&str,minutes:i64,start_minute:usize)->AppResult<api_contracts::QuoteResponse>{
 let wh=watt_hours(kwh)?;
 if !(1..=1440).contains(&minutes) || start_minute>=1440 {return Err(bad());}
 let rates=daily_rates(rule)?;
 let mut electric_rate=0i128;let mut service_rate=0i128;
 for offset in 0..minutes as usize {
  let (e,s)=rates[(start_minute+offset)%1440].ok_or_else(||AppError::BadRequest("预计充电时段缺少电价配置".into()))?;
  electric_rate+=i128::from(e);service_rate+=i128::from(s);
 }
 let denominator=i128::from(minutes)*1000;
 let rounded=|numerator:i128|->i64{((numerator+denominator/2)/denominator) as i64};
 let electric=rounded(i128::from(wh)*electric_rate);
 let mut service=if rule.mode=="minute"{0}else{rounded(i128::from(wh)*service_rate)};
 if rule.mode!="kwh"{service+=minutes*rule.service_fee_cents_per_min;}
 service=service.max(rule.min_charge_cents-electric);
 let total=electric+service;
 if total<=0 || total>i64::from(i32::MAX){return Err(bad());}
 Ok(api_contracts::QuoteResponse{electric_cents:electric,service_cents:service,total_cents:total})
}
#[cfg(test)]
mod tests{
 use super::*;use serde_json::json;
 fn rule()->DevicePricing{DevicePricing{station_id:1,station_name:"Station".into(),rule_id:1,name:"Rule".into(),version:1,mode:"kwh".into(),time_of_use:json!([{ "period":"peak","start":"08:00","end":"20:00","electric_price_cents":100,"service_price_cents":40},{"period":"off","start":"20:00","end":"08:00","electric_price_cents":50,"service_price_cents":20}]),service_fee_cents_per_kwh:30,service_fee_cents_per_min:2,min_charge_cents:0}}
 #[test] fn crossing_periods_and_midnight(){let r=estimate(&rule(),"1",120,19*60).unwrap();assert_eq!((r.electric_cents,r.service_cents,r.total_cents),(75,30,105));let r=estimate(&rule(),"0.500",120,23*60).unwrap();assert_eq!((r.electric_cents,r.service_cents),(25,10));}
 #[test] fn minimum_and_modes(){let mut p=rule();p.min_charge_cents=100;let r=estimate(&p,"0.100",60,10*60).unwrap();assert_eq!((r.electric_cents,r.service_cents,r.total_cents),(10,90,100));p.min_charge_cents=0;p.mode="mixed".into();assert_eq!(estimate(&p,"1",60,600).unwrap().total_cents,260);p.mode="minute".into();assert_eq!(estimate(&p,"1",60,600).unwrap().total_cents,220);}
 #[test] fn invalid_inputs_and_configuration(){for input in ["0","-1","NaN","1e2","0.0001","100.001","1.",".5"]{assert!(estimate(&rule(),input,60,0).is_err());}assert!(estimate(&rule(),"1",0,0).is_err());let mut p=rule();p.time_of_use=json!([{ "period":"day","start":"08:00","end":"20:00","electric_price_cents":100}]);assert!(estimate(&p,"1",60,0).is_err());let duplicate=p.time_of_use[0].clone();p.time_of_use.as_array_mut().unwrap().push(duplicate);assert!(estimate(&p,"1",60,600).is_err());}
}
