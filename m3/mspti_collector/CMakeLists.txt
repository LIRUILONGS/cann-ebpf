cmake_minimum_required(VERSION 3.14)

project(cann_mspti_collector LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 11)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

if(DEFINED ENV{ASCEND_HOME_PATH})
    set(ASCEND_PATH $ENV{ASCEND_HOME_PATH})
else()
    set(ASCEND_PATH "/usr/local/Ascend/ascend-toolkit/latest")
endif()

set(MSPTI_ROOT "${ASCEND_PATH}/tools/mspti")
find_library(MSPTI_LIB NAMES mspti PATHS "${MSPTI_ROOT}/lib64" "${ASCEND_PATH}/lib64")
if(NOT MSPTI_LIB)
    message(FATAL_ERROR "libmspti.so not found under ${MSPTI_ROOT}/lib64 or ${ASCEND_PATH}/lib64")
endif()

add_compile_options(-Wall -Wextra -Werror=return-type)

add_library(cannmspti SHARED collector.cpp)
target_include_directories(cannmspti PRIVATE
    "${MSPTI_ROOT}/include"
    "${ASCEND_PATH}/include")
target_link_libraries(cannmspti PRIVATE "${MSPTI_LIB}" pthread)
set_target_properties(cannmspti PROPERTIES
    OUTPUT_NAME cannmspti
    VERSION 0.1.0
    SOVERSION 0)

add_executable(cann-mspti-collector cli.cpp)
target_link_libraries(cann-mspti-collector PRIVATE cannmspti pthread)
set_target_properties(cann-mspti-collector PROPERTIES BUILD_RPATH "$ORIGIN")

option(BUILD_SYNTHETIC_ACL "Build the synthetic ACL validation workload" ON)
if(BUILD_SYNTHETIC_ACL)
    find_library(ASCENDCL_LIB NAMES ascendcl PATHS "${ASCEND_PATH}/lib64")
    find_library(NNOPBASE_LIB NAMES nnopbase PATHS "${ASCEND_PATH}/lib64")
    find_library(OPAPI_LIB NAMES opapi PATHS "${ASCEND_PATH}/lib64")
    if(NOT ASCENDCL_LIB OR NOT NNOPBASE_LIB OR NOT OPAPI_LIB)
        message(FATAL_ERROR "Ascend ACL/opapi libraries are incomplete under ${ASCEND_PATH}/lib64")
    endif()
    add_executable(synthetic_acl synthetic_acl.cpp)
    target_include_directories(synthetic_acl PRIVATE
        "${ASCEND_PATH}/include"
        "${ASCEND_PATH}/include/acl"
        "${ASCEND_PATH}/include/aclnn"
        "${MSPTI_ROOT}/include")
    target_link_libraries(synthetic_acl PRIVATE
        cannmspti
        "${ASCENDCL_LIB}"
        "${NNOPBASE_LIB}"
        "${OPAPI_LIB}"
        pthread)
    set_target_properties(synthetic_acl PROPERTIES BUILD_RPATH
        "$ORIGIN;${MSPTI_ROOT}/lib64;${ASCEND_PATH}/lib64")
endif()

install(TARGETS cannmspti cann-mspti-collector DESTINATION lib)
