#!/bin/bash  
cat /dev/null > ./makeresult
for((i=1;i<=10;i++));  
do   
echo "-------------start $i round----------------\n" >> ./makeresult
make project2c >> ./makeresult 2>&1
#make project2b >> ./makeresult 2>&1
done