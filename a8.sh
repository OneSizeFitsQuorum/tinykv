#!/bin/bash  
cat /dev/null > ./makeresult
for((i=1;i<=500;i++));  
do   
echo "-------------start $i round----------------\n" >> ./makeresult
make projectTestPersistConcurrent2B >> ./makeresult8 2>&1
#make project2b >> ./makeresult 2>&1
done